# Shared build helpers. PowerShell 7 runs these scripts on Windows, macOS and Linux.
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $true

function Invoke-BuildTool {
    param([string]$Tool, [string[]]$Arguments)
    & $Tool @Arguments
    if ($LASTEXITCODE -ne 0) { throw "$Tool failed with exit code $LASTEXITCODE" }
}

function Get-WindowsWorkbenchTestBase {
    param([switch]$WorkbenchProfile)
    if ($env:RUNNER_TEMP) { return [IO.Path]::GetFullPath($env:RUNNER_TEMP) }
    if ($WorkbenchProfile) {
        # The smoke profile includes project storage, which Cage rejects inside AppData.
        return Join-Path (Split-Path $PSScriptRoot -Parent) '_output/workbench-smoke'
    }
    return [IO.Path]::GetTempPath()
}

function Resolve-WindowsBuildDirectory {
    param([string]$Directory)
    $full = [IO.Path]::GetFullPath($Directory)
    $resolved = [IO.Path]::GetPathRoot($full)
    foreach ($part in ($full.Substring($resolved.Length) -split '[\\/]')) {
        if (-not $part) { continue }
        $resolved = Join-Path $resolved $part
        $info = [IO.DirectoryInfo]::new($resolved)
        if ($info.Exists -and ($info.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
            $target = $info.ResolveLinkTarget($true)
            if ($target) { $resolved = $target.FullName }
        }
    }
    return $resolved
}

function Assert-WindowsWorkbenchTestBase {
    param([string]$Directory = (Get-WindowsWorkbenchTestBase -WorkbenchProfile))
    if (-not $IsWindows) { return }
    $path = (Resolve-WindowsBuildDirectory $Directory).TrimEnd('\', '/') + '\'
    $blocked = @($env:APPDATA, $env:LOCALAPPDATA, $env:XDG_CONFIG_HOME)
    if ($env:USERPROFILE) { $blocked += Join-Path $env:USERPROFILE 'AppData' }
    foreach ($root in $blocked) {
        if (-not $root -or -not [IO.Path]::IsPathFullyQualified($root)) { continue }
        $prefix = (Resolve-WindowsBuildDirectory $root).TrimEnd('\', '/') + '\'
        if ($path.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) {
            throw 'The workbench smoke test directory resolves inside AppData or another private configuration directory. Set RUNNER_TEMP to a writable local directory outside it, such as D:\CageBuildTemp.'
        }
    }
}

function Test-WindowsLowIntegrityText {
    param([string[]]$Lines)
    if (-not $Lines) { return $false }
    foreach ($line in $Lines) {
        if (-not $line) { continue }
        if ($line -match '(?i)(?:Mandatory Label\\(?:Low Mandatory Level|低强制级别|低強制層級)|S-1-16-4096|\(ML;[^)]*;LW\))') {
            return $true
        }
    }
    return $false
}

function Assert-WindowsBuildDirectoryIntegrity {
    param([Parameter(Mandatory)][string]$Directory)
    if (-not $IsWindows) { return }
    $icacls = Get-BuildCommand @('icacls.exe', 'icacls') -Optional
    if ($icacls -and (Test-Path -LiteralPath $Directory)) {
        $savedNative = $PSNativeCommandUseErrorActionPreference
        $PSNativeCommandUseErrorActionPreference = $false
        try {
            $lines = @(& $icacls $Directory 2>$null)
            if ($LASTEXITCODE -eq 0 -and (Test-WindowsLowIntegrityText $lines)) {
                throw "Build directory '$Directory' carries Windows Low Mandatory Level (Low IL). Executables built here inherit Low IL and cannot write to %TEMP% (causing 'cage.exe verify-vm-assets' to fail with Access is denied). Copy the repository to a normal local directory without a Low Mandatory Level label (for example C:\src\Claude-Cage) and rebuild. See docs/windows.md#windows-源码构建."
            }
        } finally { $PSNativeCommandUseErrorActionPreference = $savedNative }
    }
    $whoami = Get-BuildCommand @('whoami.exe', 'whoami') -Optional
    if ($whoami) {
        $savedNative = $PSNativeCommandUseErrorActionPreference
        $PSNativeCommandUseErrorActionPreference = $false
        try {
            $groups = @(& $whoami '/groups' 2>$null)
            if ($LASTEXITCODE -eq 0 -and (Test-WindowsLowIntegrityText $groups)) {
                throw 'The current build shell runs at Windows Low Mandatory Level (Low IL), which prevents built executables from writing to %TEMP%. Run the build from a normal Medium-integrity shell in a standard local directory (for example C:\src\Claude-Cage). See docs/windows.md#windows-源码构建.'
            }
        } finally { $PSNativeCommandUseErrorActionPreference = $savedNative }
    }
}

function Invoke-InIsolatedTestTemp {
    param([Parameter(Mandatory)][scriptblock]$Checks)
    # Windows' default temp is inside AppData, which must remain protected
    # against project sharing. Put test projects in the runner's own temp.
    $directory = Join-Path (Get-WindowsWorkbenchTestBase) ('cage-go-tests-' + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $directory | Out-Null
    $savedTemp = $env:TEMP; $savedTmp = $env:TMP
    try {
        $env:TEMP = $directory; $env:TMP = $directory
        & $Checks
    } finally {
        $env:TEMP = $savedTemp; $env:TMP = $savedTmp
        Remove-Item -LiteralPath $directory -Recurse -Force -ErrorAction SilentlyContinue
    }
}

function ConvertTo-SmokeSafeText {
    param($Value)
    if ($Value -isnot [string]) { return '' }
    $text = [regex]::Replace($Value, 'https?://[^\s"''<>]+', {
        param($match)
        try { return ([Uri]$match.Value).GetLeftPart([UriPartial]::Path) }
        catch { return '[URL omitted]' }
    })
    $text = $text -replace '[?#][^\s"''<>)]*', '[redacted]'
    return $text.Substring(0, [Math]::Min(768, $text.Length))
}

function Get-WindowsWorkbenchStorageError {
    param([string]$Profile)
    if (-not $Profile) { return }
    # Only include the fixed sharing errors in public build diagnostics.
    $storageErrors = @('请选择具体项目，不要共享主目录、Cage 配置或包含配置的上级目录',
        '请选择项目根目录，不要共享 Git 等版本控制元数据目录',
        'Cage 配置、环境存储及备份不能共享',
        'Cage 配置、环境存储、迁移备份及其上级目录不能共享给虚拟机')
    foreach ($relative in @('logs/events.jsonl', 'desktop-entry/logs/events.jsonl')) {
        $log = Join-Path $Profile $relative
        foreach ($line in (Get-Content -LiteralPath $log -Tail 64 -ErrorAction SilentlyContinue)) {
            try { $event = ConvertFrom-Json -InputObject $line -AsHashtable -ErrorAction Stop }
            catch { continue }
            if ($event -is [System.Collections.IDictionary] -and $event['level'] -eq 'error' -and
                $event['source'] -eq 'command' -and $event['message'] -in $storageErrors) {
                return $event['message']
            }
        }
    }
}

function Stop-WindowsWorkbenchAcceptance {
    param([string]$Message, [string]$ControllerProfile)
    $storageError = Get-WindowsWorkbenchStorageError $ControllerProfile
    if ($storageError) { $Message += " Controller error: $storageError." }
    throw $Message
}

function Assert-WindowsWorkbenchReport {
    param([int]$ExitCode, [string]$Report, [string]$ControllerProfile)
    $reportFile = Get-Item -LiteralPath $Report -ErrorAction SilentlyContinue
    if ($reportFile -isnot [IO.FileInfo]) {
        Stop-WindowsWorkbenchAcceptance "Cage workbench acceptance failed ($ExitCode). No smoke report: $Report" $ControllerProfile
    }
    if ($reportFile.Length -gt 65536) {
        Stop-WindowsWorkbenchAcceptance "Cage smoke report exceeds the bounded diagnostic size: $Report" $ControllerProfile
    }
    try { $result = Get-Content -LiteralPath $Report -Raw | ConvertFrom-Json -AsHashtable -NoEnumerate }
    catch { $result = $null }
    if ($result -isnot [System.Collections.IDictionary]) {
        Stop-WindowsWorkbenchAcceptance "Cage workbench acceptance failed ($ExitCode). Invalid smoke report: $Report" $ControllerProfile
    }
    $failed = $ExitCode -ne 0 -or $result['success'] -ne $true -or $result['nativeBridgeReady'] -ne $true
    $safe = @{ phase = ConvertTo-SmokeSafeText $result['phase']; nativeStatus = ConvertTo-SmokeSafeText $result['nativeStatus']; jsErrors = @() }
    if ($result['prepareExit'] -is [int] -or $result['prepareExit'] -is [long]) { $safe.prepareExit = $result['prepareExit'] }
    if ($result['prepareStatus'] -in @('prepared', 'stop_required', 'error', 'unexpected')) { $safe.prepareStatus = $result['prepareStatus'] }
    if ($result['prepareMessage'] -is [string]) { $safe.prepareMessage = ConvertTo-SmokeSafeText $result['prepareMessage'] }
    if ($result['page'] -is [System.Collections.IDictionary] -and $result['page']['errors']) {
        $safe.jsErrors = @($result['page']['errors'] | Select-Object -First 8 | ForEach-Object {
            if ($_ -isnot [System.Collections.IDictionary]) { return }
            @{
                type = ConvertTo-SmokeSafeText $_['type']
                source = if ($_['source'] -is [string] -and $_['source'].StartsWith('/')) { ConvertTo-SmokeSafeText $_['source'] } else { '' }
                message = ConvertTo-SmokeSafeText $_['message']
                stack = @($_['stack'] | Select-Object -First 6 | ForEach-Object { ConvertTo-SmokeSafeText $_ })
            }
        })
    }
    if ($failed -and $ControllerProfile) {
        $storageError = Get-WindowsWorkbenchStorageError $ControllerProfile
        if ($storageError) {
            $safe.controllerError = $storageError
        }
    }
    # Log only these already-bounded fields, even when the process failed. Do
    # not print launch entries, page URLs, account data or the raw report.
    Write-Host ('Cage smoke diagnostic: ' + ($safe | ConvertTo-Json -Depth 6 -Compress))
    if ($failed) {
        $reason = $safe['controllerError'] ?? $safe.nativeStatus
        throw "Cage workbench/bridge acceptance failed ($ExitCode), phase $($safe.phase): $reason. Diagnostics: $Report"
    }
}

function Wait-WindowsWorkbenchFiles {
    param([Parameter(Mandatory)][string]$AppDirectory)
    # The log relay can outlive the controller's profile lock. Wait for the
    # isolated build's executable images to be released before moving them.
    $deadline = [DateTime]::UtcNow.AddSeconds(20)
    foreach ($relative in @('Cage.exe', 'resources/cage.exe')) {
        $file = Join-Path $AppDirectory $relative
        while ($true) {
            try {
                $handle = [IO.File]::Open($file, [IO.FileMode]::Open, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
                $handle.Dispose()
                break
            } catch [IO.IOException], [UnauthorizedAccessException] {
                if ([DateTime]::UtcNow -ge $deadline) { throw "Smoke executable is still in use: $relative" }
                Start-Sleep -Milliseconds 100
            }
        }
    }
}

function Assert-WindowsWorkbench {
    param([Parameter(Mandatory)][string]$AppDirectory)
    if (-not $IsWindows) { throw 'Workbench acceptance requires Windows.' }
    $base = Get-WindowsWorkbenchTestBase -WorkbenchProfile
    Assert-WindowsWorkbenchTestBase $base
    $testRoot = Join-Path $base ('cage-build-ui-' + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $testRoot -Force | Out-Null
    $report = Join-Path $testRoot 'smoke.json'
    $profile = Join-Path $testRoot 'profile'
    $start = [Diagnostics.ProcessStartInfo]::new((Join-Path $AppDirectory 'Cage.exe'))
    $start.UseShellExecute = $false
    $start.WorkingDirectory = $AppDirectory
    $start.Environment['CLAUDE_CAGE_CONFIG'] = Join-Path $profile 'config.json'
    $start.ArgumentList.Add('--smoke-test')
    $start.ArgumentList.Add($report)
    $window = [Diagnostics.Process]::Start($start)
    $passed = $false
    try {
        if (-not $window.WaitForExit(180000)) {
            Stop-WindowsWorkbenchAcceptance "Cage workbench acceptance timed out. Diagnostics retained at $testRoot" $profile
        }
        Assert-WindowsWorkbenchReport -ExitCode $window.ExitCode -Report $report -ControllerProfile $profile
        Wait-WindowsWorkbenchFiles -AppDirectory $AppDirectory
        $passed = $true
    } finally {
        if (-not $window.HasExited) { $window.Kill($true); $window.WaitForExit() }
        $window.Dispose()
        if (-not $passed) {
            # Close only this test's authenticated controller. Killing every
            # cage.exe could terminate another installed application or VM.
            try {
                $cleanup = [Diagnostics.ProcessStartInfo]::new((Join-Path $AppDirectory 'resources/cage.exe'))
                $cleanup.UseShellExecute = $false
                $cleanup.CreateNoWindow = $true
                $cleanup.RedirectStandardOutput = $true
                $cleanup.RedirectStandardError = $true
                $cleanup.Environment['CLAUDE_CAGE_CONFIG'] = Join-Path $profile 'config.json'
                $cleanup.ArgumentList.Add('prepare-update'); $cleanup.ArgumentList.Add('--json')
                $controller = [Diagnostics.Process]::Start($cleanup)
                try {
                    $stdout = $controller.StandardOutput.ReadToEndAsync()
                    $stderr = $controller.StandardError.ReadToEndAsync()
                    if (-not $controller.WaitForExit(20000)) { $controller.Kill($true); $controller.WaitForExit() }
                    if ($controller.ExitCode -ne 0) { Write-Warning 'The isolated smoke controller did not confirm shutdown; its profile is retained.' }
                } finally { $controller.Dispose() }
            } catch { Write-Warning "Isolated smoke cleanup failed: $($_.Exception.Message)" }
        }
        # A successful smoke report is written only after the isolated backend
        # confirms shutdown. Failed profiles remain available for diagnosis.
        if ($passed) { Remove-Item -LiteralPath $testRoot -Recurse -Force -ErrorAction SilentlyContinue }
    }
}

function Get-CageVersion {
    param([string]$Repository)
    $value = (Get-Content -LiteralPath (Join-Path $Repository 'VERSION') -Raw).Trim()
    if ($value -notmatch '^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$') { throw 'Invalid VERSION.' }
    return $value
}

function Get-BuildCommand {
    param([string[]]$Names, [switch]$Optional)
    foreach ($name in $Names) {
        $command = Get-Command $name -ErrorAction SilentlyContinue
        if ($command) { return $command.Source }
    }
    if (-not $Optional) { throw "Missing build tool: $($Names -join ' or ')" }
}

function Publish-WindowsCacheFile {
    param([string]$Candidate, [string]$Path)
    # Another build may have published a verified copy while this one downloaded.
    try { [IO.File]::Move($Candidate, $Path) }
    catch [IO.IOException] { if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { throw } }
}

function Get-VerifiedDependency {
    param($Dependency, [string]$Cache)
    if ($Dependency.url -notlike 'https://*') { throw 'Build dependencies must use HTTPS.' }
    $path = Join-Path $Cache $Dependency.file
    $fetcher = Join-Path $PSScriptRoot 'fetch-build-artifact.mjs'
    $node = Get-BuildCommand @('node.exe', 'node') -Optional
    if (-not (Test-Path -LiteralPath $path -PathType Leaf) -and $node -and (Test-Path -LiteralPath $fetcher -PathType Leaf)) {
        # The shared fetcher resumes with HTTP Range after each interruption and
        # only publishes a file whose pinned digest matches.
        New-Item -ItemType Directory -Path $Cache -Force | Out-Null
        try {
            Invoke-BuildTool $node @($fetcher, $Dependency.url, $Dependency.hash, $path)
        } catch {
            if (-not $IsWindows) { throw }
            Write-Warning "Shared curl downloader failed for $($Dependency.file); falling back to Invoke-WebRequest with HTTPS and checksum verification: $($_.Exception.Message)"
        }
    }
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        New-Item -ItemType Directory -Path $Cache -Force | Out-Null
        $partial = "$path.partial-$([guid]::NewGuid().ToString('N'))"
        try {
            # Release hosts and CDNs drop connections now and then. Retry the
            # transfer; the checksum below still decides what is accepted.
            for ($attempt = 1; ; $attempt++) {
                try {
                    Invoke-WebRequest -Uri $Dependency.url -OutFile $partial -TimeoutSec 900 -MaximumRedirection 5
                    break
                } catch {
                    if ($attempt -ge 4) { throw }
                    Write-Warning "Download of $($Dependency.file) failed (attempt $attempt): $($_.Exception.Message)"
                    Start-Sleep -Seconds (5 * $attempt)
                }
            }
            $actual = (Get-FileHash -LiteralPath $partial -Algorithm $Dependency.algorithm).Hash
            if ($actual -ne $Dependency.hash) { throw "Downloaded dependency checksum mismatch: $($Dependency.file)" }
            Publish-WindowsCacheFile $partial $path
        } finally {
            if (Test-Path -LiteralPath $partial) { Remove-Item -LiteralPath $partial -Force }
        }
    }
    $actual = (Get-FileHash -LiteralPath $path -Algorithm $Dependency.algorithm).Hash
    if ($actual -ne $Dependency.hash) { throw "Dependency checksum mismatch: $($Dependency.file). Remove the cached file and retry." }
    return $path
}

function Assert-SevenZipFormats {
    param([string]$SevenZip)
    # Minimal builds named 7z.exe can lack the PE/NSIS handlers needed for the
    # pinned QEMU installer; find out before the long QEMU compile.
    $info = (& $SevenZip 'i' 2>&1 | Out-String)
    $missing = @(@('PE', 'Nsis') | Where-Object { $info -cnotmatch "(?m)\s$_\s" })
    if ($LASTEXITCODE -ne 0 -or $missing.Count) {
        throw "7-Zip at $SevenZip does not list the $($missing -join ', ') archive formats. Install the full 7-Zip and put its directory first on PATH. See docs/windows.md#windows-源码构建."
    }
}

function Expand-BuildArchive {
    param([string]$Archive, [string]$Destination, [string]$SevenZip)
    New-Item -ItemType Directory -Path $Destination -Force | Out-Null
    Invoke-BuildTool $SevenZip @('x', '-y', '-bso0', '-bsp0', "-o$Destination", $Archive)
}

function Save-ReleaseSourceTar {
    param([string]$Uri, [string]$Tar)
    if ($Uri -notlike 'https://*') { throw 'Source archive downloads must use HTTPS.' }
    $gzip = "$Tar.gz"
    try {
        Invoke-WebRequest -Uri $Uri -OutFile $gzip -TimeoutSec 120 -MaximumRedirection 5 -ErrorAction Stop
    } catch {
        Write-Warning "Release source download failed. Falling back to Git: $($_.Exception.Message)"
        return $false
    }
    $inputStream = [IO.File]::OpenRead($gzip)
    try {
        $stream = [IO.Compression.GZipStream]::new($inputStream, [IO.Compression.CompressionMode]::Decompress)
        try {
            $outputStream = [IO.File]::Create($Tar)
            try { $stream.CopyTo($outputStream) } finally { $outputStream.Dispose() }
        } finally { $stream.Dispose() }
    } finally { $inputStream.Dispose() }
    return $true
}

function Get-VerifiedGitArchive {
    param($Dependency, [string]$Cache)
    $archive = Join-Path $Cache $Dependency.file
    if (-not (Test-Path -LiteralPath $archive -PathType Leaf)) {
        $stage = Join-Path $Cache ('source-' + [guid]::NewGuid().ToString('N'))
        New-Item -ItemType Directory -Path $stage -Force | Out-Null
        $partial = Join-Path $stage 'source.tar'
        try {
            $fromRelease = $Dependency.PSObject.Properties['archiveUrl'] -and (Save-ReleaseSourceTar $Dependency.archiveUrl $partial)
            if (-not $fromRelease) {
                $git = Get-BuildCommand @('git.exe', 'git')
                $checkout = Join-Path $stage 'git'
                Invoke-BuildTool $git @('init', '--bare', '--quiet', $checkout)
                for ($attempt = 1; ; $attempt++) {
                    try {
                        Invoke-BuildTool $git @('--git-dir', $checkout, 'fetch', '--depth=1', $Dependency.repository, $Dependency.commit)
                        break
                    } catch {
                        if ($attempt -ge 3) { throw }
                        Write-Warning "Fetching the pinned QEMU source failed (attempt $attempt). Retrying."
                        Start-Sleep -Seconds (2 * $attempt)
                    }
                }
                $actualCommit = & $git '--git-dir' $checkout 'rev-parse' 'FETCH_HEAD'
                if ($LASTEXITCODE -ne 0 -or $actualCommit -ne $Dependency.commit) { throw 'Fetched source commit does not match the pinned QEMU build.' }
                # Keep the pinned tar identical to the reviewed POSIX-generated archive.
                Invoke-BuildTool $git @('--git-dir', $checkout, '-c', 'core.autocrlf=false', '-c', 'tar.umask=0002', 'archive', '--format=tar', "--prefix=$($Dependency.prefix)", "--output=$partial", $Dependency.commit)
            }
            $actualHash = (Get-FileHash -LiteralPath $partial -Algorithm SHA256).Hash.ToLowerInvariant()
            if ($actualHash -ne $Dependency.hash) { throw "Source archive checksum mismatch: $($Dependency.file); expected $($Dependency.hash), actual $actualHash" }
            Publish-WindowsCacheFile $partial $archive
        } finally {
            Remove-Item -LiteralPath $stage -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
    $actualHash = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actualHash -ne $Dependency.hash) { throw "Source archive checksum mismatch: $($Dependency.file); expected $($Dependency.hash), actual $actualHash. Remove the cached file and retry." }
    return $archive
}

function Invoke-WindowsQemuProbe {
    param([string]$Executable, [string[]]$Arguments)
    $start = [Diagnostics.ProcessStartInfo]::new([IO.Path]::GetFullPath($Executable))
    $start.UseShellExecute = $false
    $start.CreateNoWindow = $true
    $start.WorkingDirectory = Split-Path $start.FileName -Parent
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    # QEMU uses the Windows ANSI code page for redirected path output. Decode
    # just this pipe instead of changing the calling shell's output encoding.
    $codePage = Get-ItemPropertyValue -LiteralPath 'HKLM:\SYSTEM\CurrentControlSet\Control\Nls\CodePage' -Name ACP
    [Text.Encoding]::RegisterProvider([Text.CodePagesEncodingProvider]::Instance)
    $start.StandardOutputEncoding = [Text.Encoding]::GetEncoding([int]$codePage)
    $start.StandardErrorEncoding = $start.StandardOutputEncoding
    # Catch missing DLLs even on a host with MSYS2 or another QEMU on PATH.
    $start.Environment['PATH'] = [Environment]::SystemDirectory
    foreach ($argument in $Arguments) { $start.ArgumentList.Add($argument) }
    $process = [Diagnostics.Process]::Start($start)
    try {
        $stdout = $process.StandardOutput.ReadToEndAsync()
        $stderr = $process.StandardError.ReadToEndAsync()
        if (-not $process.WaitForExit(15000)) {
            $process.Kill()
            throw "QEMU runtime probe timed out: $([IO.Path]::GetFileName($Executable))."
        }
        if ($process.ExitCode -ne 0) {
            throw "QEMU runtime probe failed: $([IO.Path]::GetFileName($Executable)) (0x$($process.ExitCode.ToString('X8'))). $($stderr.GetAwaiter().GetResult().Trim())"
        }
        return $stdout.GetAwaiter().GetResult()
    } finally { $process.Dispose() }
}

function Enter-WindowsQemuOutputLock {
    param([string]$Directory)
    $deadline = [DateTime]::UtcNow.AddMinutes(30)
    while ($true) {
        try { return [IO.File]::Open("$Directory.lock", [IO.FileMode]::OpenOrCreate, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None) }
        catch [IO.IOException] {
            if ($IsWindows -and ($_.Exception.HResult -band 0xffff) -notin @(32, 33)) { throw }
            if ([DateTime]::UtcNow -ge $deadline) { throw "Timed out waiting for the QEMU runtime cache lock: $Directory.lock" }
            Start-Sleep -Milliseconds 250
        }
    }
}

function Test-WindowsQemuBuildManifest {
    param([string]$Directory, [string]$Fingerprint)
    try {
        $root = Get-Item -LiteralPath $Directory
        if ($root -isnot [IO.DirectoryInfo] -or ($root.Attributes -band [IO.FileAttributes]::ReparsePoint)) { return $false }
        $files = @{}
        foreach ($file in (Get-ChildItem -LiteralPath $Directory -Force)) {
            if ($file -isnot [IO.FileInfo] -or ($file.Attributes -band [IO.FileAttributes]::ReparsePoint)) { return $false }
            $files[$file.Name] = $file.FullName
        }
        foreach ($name in @('cage-build.json', 'qemu-system-x86_64.exe', 'qemu-img.exe', 'build-packages.txt', 'build-source-packages.txt')) {
            if (-not $files.ContainsKey($name)) { return $false }
        }
        $manifest = Get-Content -LiteralPath $files['cage-build.json'] -Raw | ConvertFrom-Json -AsHashtable -NoEnumerate
        if ($manifest -isnot [System.Collections.IDictionary] -or $manifest['fingerprint'] -isnot [string] -or
            $manifest['fingerprint'] -notmatch '^[0-9a-fA-F]{64}$' -or
            ($Fingerprint -and $manifest['fingerprint'] -ne $Fingerprint) -or $manifest['files'] -isnot [array]) { return $false }
        $files.Remove('cage-build.json')
        foreach ($entry in $manifest['files']) {
            if ($entry -isnot [System.Collections.IDictionary] -or $entry['name'] -isnot [string] -or $entry['sha256'] -isnot [string] -or
                -not $files.ContainsKey($entry['name'])) { return $false }
            if ((Get-FileHash -LiteralPath $files[$entry['name']] -Algorithm SHA256).Hash -ne $entry['sha256']) { return $false }
            $files.Remove($entry['name'])
        }
        return $files.Count -eq 0
    } catch {
        return $false
    }
}

function Publish-WindowsQemuRuntime {
    param([string]$StagedDirectory, [string]$Directory, [switch]$ManagedCache)
    # The caller holds the per-output lock and stages the runtime on this volume.
    $previous = $null
    if (Test-Path -LiteralPath $Directory) {
        $current = Get-Item -LiteralPath $Directory
        if ($current -isnot [IO.DirectoryInfo] -or ($current.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
            throw "Choose a normal directory for QEMU build output: $Directory"
        }
        $empty = -not (Get-ChildItem -LiteralPath $Directory -Force | Select-Object -First 1)
        $marker = Get-Item -LiteralPath (Join-Path $Directory 'cage-build.json') -ErrorAction SilentlyContinue
        if (-not $ManagedCache -and -not $empty -and
            ($marker -isnot [IO.FileInfo] -or ($marker.Attributes -band [IO.FileAttributes]::ReparsePoint))) {
            throw "Choose an empty directory or an existing Cage QEMU build output: $Directory"
        }
        $removePrevious = $empty -or (Test-WindowsQemuBuildManifest $Directory)
        $previous = "$Directory.previous-$([guid]::NewGuid().ToString('N'))"
        [IO.Directory]::Move($Directory, $previous)
    }
    try { [IO.Directory]::Move($StagedDirectory, $Directory) }
    catch {
        if ($previous) { [IO.Directory]::Move($previous, $Directory) }
        throw
    }
    if ($previous) {
        if ($removePrevious) {
            try { Remove-Item -LiteralPath $previous -Recurse -Force }
            catch { Write-Warning "The previous QEMU output is retained at $previous." }
        } else {
            Write-Warning "The previous QEMU output was incomplete or contained extra files and is retained at $previous."
        }
    }
}

function Assert-WindowsQemuRuntime {
    param([string]$Directory, [switch]$Firmware)
    foreach ($binary in @('qemu-system-x86_64.exe', 'qemu-img.exe')) {
        $version = Invoke-WindowsQemuProbe (Join-Path $Directory $binary) @('--version')
        if ($version -notmatch 'version \d+\.\d+\.\d+') { throw "Invalid QEMU version output from $binary." }
    }
    if ($Firmware) {
        $directories = (Invoke-WindowsQemuProbe (Join-Path $Directory 'qemu-system-x86_64.exe') @('-L', 'help')) -split '\r?\n'
        $bundled = [IO.Path]::GetFullPath((Join-Path $Directory 'share')).TrimEnd('\', '/')
        $found = $directories | Where-Object { $_ -and [IO.Path]::GetFullPath($_).TrimEnd('\', '/') -eq $bundled }
        if (-not $found) { throw 'QEMU cannot locate its bundled firmware after relocation.' }
    }
}

function Get-WindowsQemuBinaries {
    param([string]$Directory)
    foreach ($binary in @('qemu-system-x86_64.exe', 'qemu-img.exe')) {
        if (-not (Test-Path -LiteralPath (Join-Path $Directory $binary) -PathType Leaf)) { throw "Missing QEMU runtime executable: $binary" }
    }
    return Get-ChildItem -LiteralPath $Directory -File -Recurse | Where-Object Extension -in @('.exe', '.dll')
}

function Assert-CageReleaseSignature {
    param([string]$Path)
    $signature = Get-AuthenticodeSignature -LiteralPath $Path
    if ($signature.Status -ne 'Valid' -or -not $signature.TimeStamperCertificate -or
        $signature.SignerCertificate.Thumbprint -ne $env:CAGE_WINDOWS_CERT_SHA1) {
        throw "Invalid or missing timestamped release signature: $Path"
    }
}

function Get-WindowsQemuSourceManifestHash {
    param([string]$Archive)
    # Read only the raw manifest, without extracting arbitrary archive paths.
    $start = [Diagnostics.ProcessStartInfo]::new((Join-Path $env:SystemRoot 'System32/tar.exe'))
    $start.UseShellExecute = $false
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    foreach ($arg in @('-xqOf', [IO.Path]::GetFullPath($Archive), 'source/cage/cage-build.json')) {
        $start.ArgumentList.Add($arg)
    }
    $process = [Diagnostics.Process]::Start($start)
    try {
        $errors = $process.StandardError.ReadToEndAsync()
        $hash = [Security.Cryptography.SHA256]::HashData($process.StandardOutput.BaseStream)
        $process.WaitForExit()
        if ($process.ExitCode -ne 0) { throw "Could not read the QEMU source manifest: $($errors.GetAwaiter().GetResult().Trim())" }
        return [Convert]::ToHexString($hash)
    } finally { $process.Dispose() }
}

function Get-SigningTool {
    $command = Get-Command signtool.exe -ErrorAction SilentlyContinue
    if ($command) { return $command.Source }
    if ($IsWindows) {
        $kits = Join-Path ${env:ProgramFiles(x86)} 'Windows Kits/10/bin'
        $candidate = Get-ChildItem -LiteralPath $kits -Directory -ErrorAction SilentlyContinue |
            Sort-Object Name -Descending | ForEach-Object { Join-Path $_.FullName 'x64/signtool.exe' } |
            Where-Object { Test-Path -LiteralPath $_ } | Select-Object -First 1
        if ($candidate) { return $candidate }
    }
    throw 'Release signing requires Windows SDK signtool.exe on Windows.'
}

function Assert-WindowsSigningConfiguration {
    if (-not $IsWindows) { throw 'Trusted Windows build verification requires Windows.' }
    $null = Get-SigningTool
    if ($env:CAGE_WINDOWS_SIGNING_PROVIDER -eq 'esigner') {
        if ($env:CAGE_ESIGNER_CREDENTIAL_ID -notmatch '^[0-9A-Fa-f-]{36}$' -or
            $env:CAGE_ESIGNER_CERTIFICATE_SERIAL -notmatch '^[0-9A-Fa-f]{32,40}$') {
            throw 'eSigner requires CAGE_ESIGNER_CREDENTIAL_ID and the expected CAGE_ESIGNER_CERTIFICATE_SERIAL.'
        }
    } elseif ($env:CAGE_WINDOWS_SIGNING_PROVIDER -and $env:CAGE_WINDOWS_SIGNING_PROVIDER -ne 'certificate-store') {
        throw 'Unknown CAGE_WINDOWS_SIGNING_PROVIDER; use esigner or certificate-store.'
    } elseif ($env:CAGE_WINDOWS_CERT_SHA1 -notmatch '^[0-9A-Fa-f]{40}$') {
        throw 'Certificate-store signing requires CAGE_WINDOWS_CERT_SHA1.'
    }
}

function Sign-CageFile {
    param([string]$Path, [string]$SignTool)
    Assert-WindowsSigningConfiguration
    if ($env:CAGE_WINDOWS_SIGNING_PROVIDER -eq 'esigner') {
        $Path = [IO.Path]::GetFullPath($Path)
        $stage = Join-Path ([IO.Path]::GetTempPath()) ('cage-signed-' + [guid]::NewGuid().ToString('N'))
        try {
            Invoke-BuildTool (Get-BuildCommand @('node.exe', 'node')) @((Join-Path $PSScriptRoot 'esigner.mjs'), 'sign', $Path, '--output-dir', $stage)
            $signed = Join-Path $stage ([IO.Path]::GetFileName($Path))
            Invoke-BuildTool $SignTool @('verify', '/pa', '/all', '/tw', $signed)
            $signature = Get-AuthenticodeSignature -LiteralPath $signed
            if ($signature.Status -ne 'Valid' -or -not $signature.TimeStamperCertificate -or
                $signature.SignerCertificate.SerialNumber -ne $env:CAGE_ESIGNER_CERTIFICATE_SERIAL -or
                ($env:CAGE_WINDOWS_CERT_SHA1 -and $signature.SignerCertificate.Thumbprint -ne $env:CAGE_WINDOWS_CERT_SHA1)) {
                throw 'eSigner output lacks the expected trusted publisher certificate or timestamp. Original file retained.'
            }
            $env:CAGE_WINDOWS_CERT_SHA1 = $signature.SignerCertificate.Thumbprint
            Copy-Item -LiteralPath $signed -Destination $Path -Force
        } finally { if (Test-Path -LiteralPath $stage) { Remove-Item -LiteralPath $stage -Recurse -Force } }
    } else {
        Invoke-BuildTool $SignTool @('sign', '/sha1', $env:CAGE_WINDOWS_CERT_SHA1, '/fd', 'SHA256',
            '/tr', 'http://timestamp.digicert.com', '/td', 'SHA256', '/d', 'Cage', $Path)
        Invoke-BuildTool $SignTool @('verify', '/pa', '/all', $Path)
    }
}
