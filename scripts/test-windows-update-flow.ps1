#requires -Version 7.0
# Runs real updater/helper/NSIS transactions on an elevated disposable runner.
# Release transport and workbench acknowledgement use headless local fixtures.
# WebView2 must already be installed by the owning CI job.
[CmdletBinding()]
param([switch]$DisposableMachine)
$ErrorActionPreference = 'Stop'
if (!$IsWindows) { throw 'Complete updater transactions require Windows.' }
if ($env:GITHUB_ACTIONS -ne 'true' -and !$DisposableMachine) { throw 'Use -DisposableMachine only on an isolated Windows test machine.' }
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
try { $administrator = [Security.Principal.WindowsPrincipal]::new($identity).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator) }
finally { $identity.Dispose() }
if (!$administrator) { throw 'Use an elevated disposable Windows runner for temporary test certificate trust.' }
$repository = Split-Path $PSScriptRoot -Parent
$registration = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\ClaudeCage'
if (Test-Path $registration) { throw 'Use a test account without an installed Cage application.' }
$root = Join-Path ([IO.Path]::GetTempPath()) ('cage-update-flow-' + [guid]::NewGuid().ToString('N'))
$reportBase = if ($env:RUNNER_TEMP) { $env:RUNNER_TEMP } else { [IO.Path]::GetTempPath() }
$reportPath = Join-Path $reportBase ('cage-smoke-update-' + [guid]::NewGuid().ToString('N') + '.json')
$certificate = $null
$passed = $false
$results = @()
function Check([bool]$Value, [string]$Message) { if (!$Value) { throw $Message } }
function Write-Text([string]$Path, [string]$Value) {
    [IO.Directory]::CreateDirectory((Split-Path $Path -Parent)) | Out-Null
    [IO.File]::WriteAllText($Path, $Value)
}
function Wait-File([string]$Path) {
    $deadline = [DateTime]::UtcNow.AddMinutes(3)
    while ($true) {
        if ((Split-Path $Path -Leaf) -ne 'parent-failed.json' -and (Test-Path -LiteralPath (Join-Path $profile 'parent-failed.json'))) {
            $failure = Get-Content -LiteralPath (Join-Path $profile 'parent-failed.json') -Raw | ConvertFrom-Json
            throw "Native updater rejected the fixture: $($failure.message)"
        }
        if (Test-Path -LiteralPath $Path) {
            try {
                $value = Get-Content -LiteralPath $Path -Raw | ConvertFrom-Json
                if ($null -ne $value) { return $value }
            } catch [IO.IOException] { } catch [ArgumentException] { }
        }
        if ([DateTime]::UtcNow -ge $deadline) { throw "Update transaction timed out waiting for $Path" }
        Start-Sleep -Milliseconds 100
    }
}
function Run-Tool([string]$File, [string[]]$Arguments) {
    & $File @Arguments | Out-Host
    if ($LASTEXITCODE -ne 0) { throw "$File failed with exit $LASTEXITCODE" }
}
function Sign([string]$Path) {
    $signature = Set-AuthenticodeSignature -LiteralPath $Path -Certificate $certificate -HashAlgorithm SHA256
    Check ($signature.Status -eq 'Valid') "Fixture signature was not trusted: $Path ($($signature.Status))"
}
function Start-Fixture([string]$Executable, [string]$Profile, [string[]]$Arguments) {
    $start = [Diagnostics.ProcessStartInfo]::new($Executable)
    $start.UseShellExecute = $false
    $start.WorkingDirectory = $root
    # Test relative-to-absolute profile inheritance through the real launcher.
    $start.Environment['CLAUDE_CAGE_CONFIG'] = [IO.Path]::GetRelativePath($root, (Join-Path $Profile 'config.json'))
    foreach ($argument in $Arguments) { $start.ArgumentList.Add($argument) }
    return [Diagnostics.Process]::Start($start)
}
function Run-Installer([string]$Installer, [string]$Target) {
    $start = [Diagnostics.ProcessStartInfo]::new($Installer)
    $start.UseShellExecute = $false
    $start.WorkingDirectory = $root
    $start.Arguments = '/S /DESKTOPSHORTCUT=0 /STARTMENUSHORTCUT=0 /D=' + $Target
    $process = [Diagnostics.Process]::Start($start)
    try {
        Check ($process.WaitForExit(180000)) 'The fixture installer timed out.'
        Check ($process.ExitCode -eq 0) "The fixture installer exited $($process.ExitCode)."
    } finally { $process.Dispose() }
}
function Build-Package([string]$Version) {
    $directory = Join-Path $root $Version
    $app = Join-Path $directory 'app'
    Run-Tool 'dotnet' @('publish', (Join-Path $repository 'desktop/windows.update-tests/UpdateFlow.csproj'), '-c', 'Release', '-r', 'win-x64',
        '--self-contained', 'true', '-p:PublishSingleFile=true', '-p:IncludeNativeLibrariesForSelfExtract=true',
        "-p:Version=$Version", "-p:InformationalVersion=$Version", '-o', $app)
    Sign (Join-Path $app 'Cage.exe')
    [IO.Directory]::CreateDirectory((Join-Path $app 'resources')) | Out-Null
    Copy-Item -LiteralPath (Join-Path $app 'Cage.exe') -Destination (Join-Path $app 'resources/cage.exe')
    Write-Text (Join-Path $app 'Cage.install-id') 'io.github.lee-xeo.claude-cage'
    Write-Text (Join-Path $app 'version.txt') $Version
    Write-Text (Join-Path $app 'LICENSE.txt') 'Isolated updater transaction fixture; not a Cage distribution.'
    $files = @(Get-ChildItem -LiteralPath $app -File -Recurse | ForEach-Object {
        @{path = [IO.Path]::GetRelativePath($app, $_.FullName).Replace('\','/'); sha256 = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant()}
    })
    @{version = 1; files = $files} | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath (Join-Path $app 'Cage.install-files.json')
    $setup = Join-Path $directory 'setup.exe'
    $compiler = (Get-Command makensis.exe -ErrorAction Stop).Source
    Run-Tool $compiler @('/V2', '/INPUTCHARSET', 'UTF8', "/DAPP_DIR=$app", "/DVERSION=$Version", "/DVERSION_NUMERIC=$Version.0",
        "/DFILE_MANIFEST=$app\Cage.install-files.json", "/DOUTPUT_FILE=$setup", (Join-Path $repository 'packaging/windows/Cage.nsi'))
    Sign $setup
    return @{setup = $setup; app = $app}
}

try {
    [IO.Directory]::CreateDirectory($root) | Out-Null
    Write-Host 'Creating a temporary test signing certificate.'
    $certificate = New-SelfSignedCertificate -Type CodeSigningCert -Subject ('CN=Updater transaction ' + [guid]::NewGuid().ToString('N')) -CertStoreLocation Cert:\CurrentUser\My
    $publicCertificate = [Security.Cryptography.X509Certificates.X509Certificate2]::new($certificate.RawData)
    try {
        foreach ($storeName in @('Root','TrustedPublisher')) {
            # CurrentUser Root prompts for interactive confirmation even through
            # X509Store.Add. Only public trust goes into the disposable machine.
            Write-Host "Trusting the temporary signer in LocalMachine/$storeName."
            $store = [Security.Cryptography.X509Certificates.X509Store]::new($storeName, 'LocalMachine')
            try { $store.Open('ReadWrite'); $store.Add($publicCertificate) } finally { $store.Dispose() }
        }
    } finally { $publicCertificate.Dispose() }
    Write-Host 'Building signed old and new fixture packages.'
    $old = Build-Package '0.1.7'
    $new = Build-Package '0.1.8'
    foreach ($mode in @('success','installer-failure','startup-failure','signature-failure','digest-failure')) {
        Write-Host "Running Windows update transaction: $mode"
        $scenario = Join-Path $root $mode
        $target = Join-Path $scenario 'installed application'
        $profile = Join-Path $scenario 'selected profile'
        $installer = $new.setup
        if ($mode -eq 'signature-failure') {
            [IO.Directory]::CreateDirectory($scenario) | Out-Null
            $installer = Join-Path $scenario 'tampered-setup.exe'
            $bytes = [IO.File]::ReadAllBytes($new.setup)
            $bytes[1024] = $bytes[1024] -bxor 1
            [IO.File]::WriteAllBytes($installer, $bytes)
        }
        Write-Text (Join-Path $profile 'fixture.json') (@{mode = $mode; installer = $installer} | ConvertTo-Json)
        Run-Installer $old.setup $target
        $originalHash = (Get-FileHash -LiteralPath (Join-Path $target 'Cage.exe')).Hash
        $personal = @{
            'account.json' = 'existing-account'; 'favorites.json' = 'existing-favorites';
            'credentials/private.txt' = 'existing-login'; 'config.json' = 'existing-config'
        }
        foreach ($entry in $personal.GetEnumerator()) { Write-Text (Join-Path $profile $entry.Key) $entry.Value }
        Write-Text (Join-Path $target 'my-project/work.txt') 'original project'
        Write-Text (Join-Path $target 'CageData/state/persistent.img') 'existing-disk'
        Write-Text (Join-Path $scenario 'outside-project/file.txt') 'outside-project'
        $linkCommand = 'mklink /J "' + (Join-Path $target 'linked-project') + '" "' + (Join-Path $scenario 'outside-project') + '"'
        & cmd.exe /d /c $linkCommand | Out-Null
        Check ($LASTEXITCODE -eq 0) 'Could not create the isolated project junction.'
        $holder = $null; $parent = $null; $helper = $null; $disk = $null
        try {
            if ($mode -eq 'success') {
                # A registered VM can keep its disk open while the update runs.
                # CageData stays in place, so the open disk must not block it (#108).
                $disk = [IO.File]::Open((Join-Path $target 'CageData/state/persistent.img'), 'Open', 'Read', 'Read')
            }
            if ($mode -eq 'installer-failure') {
                $holder = Start-Fixture (Join-Path $old.app 'Cage.exe') $profile @('--hold-directory', $target)
                $null = Wait-File (Join-Path $profile 'held.json')
            }
            $parent = Start-Fixture (Join-Path $target 'Cage.exe') $profile @('--start-update')
            $rejected = $mode -in @('signature-failure','digest-failure')
            if ($rejected) {
                $failure = Wait-File (Join-Path $profile 'parent-failed.json')
                Check ($failure.phase -eq 'failed') 'An invalid update did not report failure.'
                Check ($parent.WaitForExit(30000) -and $parent.ExitCode -eq 1) 'The fixture did not finish after rejecting an invalid update.'
                Check (!(Test-Path -LiteralPath (Join-Path $profile 'helper.json'))) 'An invalid update started the installer helper.'
                Check (!(Test-Path -LiteralPath (Join-Path $profile 'handoff-0.1.7.json'))) 'An invalid update stopped the original environment.'
            } else {
                $helperInfo = Wait-File (Join-Path $profile 'helper.json')
                Check ($helperInfo.directory.TrimEnd('\') -eq (Join-Path $profile 'updates').TrimEnd('\')) 'The real AppUpdater launched its helper inside the installation directory.'
                try {
                    $helper = [Diagnostics.Process]::GetProcessById($helperInfo.pid)
                    # Pin the live process handle before it exits. PID lookup
                    # alone cannot retain the exit code after Windows reaps it.
                    $null = $helper.SafeHandle
                } catch [ArgumentException] { $helper = $null }
                Check ($parent.WaitForExit(180000)) 'The parent did not hand off installation.'
                Check ($parent.ExitCode -eq 0) 'The real AppUpdater failed before committing the installation.'
                $finished = Wait-File (Join-Path $profile 'helper-finished.json')
                if ($helper) { Check ($helper.WaitForExit(180000)) 'The updater helper did not exit.' }
                $result = Wait-File (Join-Path $profile 'updates/last-result.json')
                Check ([bool]$result.success -eq ($mode -eq 'success')) "Unexpected installation result: $($result.message)"
                Check ($finished.exit -eq $(if ($mode -eq 'success') { 0 } else { 1 })) 'The helper result and transaction return value disagreed.'
                if ($helper) { Check ($helper.ExitCode -eq $finished.exit) "The helper process exit ($($helper.ExitCode)) disagreed with the transaction result ($($finished.exit))." }
            }
            if ($mode -eq 'success') {
                $null = Wait-File (Join-Path $profile 'active.json')
                Check ([Diagnostics.FileVersionInfo]::GetVersionInfo((Join-Path $target 'Cage.exe')).ProductVersion -eq '0.1.8') 'The update did not install the requested version.'
            } elseif (!$rejected) {
                $reopened = Wait-File (Join-Path $profile 'reopened.json')
                Check ($reopened.version -eq '0.1.7' -and $reopened.profile -eq $profile) 'The original application did not reopen with its original profile.'
            }
            if ($mode -ne 'success') {
                Check ((Get-FileHash -LiteralPath (Join-Path $target 'Cage.exe')).Hash -eq $originalHash) 'The failed update did not retain the original program bytes.'
            }
            $project = if ($mode -eq 'startup-failure') { 'saved by new version' } else { 'original project' }
            Check ((Get-Content -LiteralPath (Join-Path $target 'my-project/work.txt') -Raw) -eq $project) 'Update or rollback lost the latest project contents.'
            Check ((Get-Content -LiteralPath (Join-Path $target 'CageData/state/persistent.img') -Raw) -eq 'existing-disk') 'Persistent data in the installation directory changed.'
            Check ((Get-Item -LiteralPath (Join-Path $target 'linked-project')).LinkType -eq 'Junction') 'Update or rollback replaced the project junction.'
            Check ((Get-Content -LiteralPath (Join-Path $target 'linked-project/file.txt') -Raw) -eq 'outside-project') 'The project junction target changed.'
            foreach ($entry in $personal.GetEnumerator()) { Check ((Get-Content -LiteralPath (Join-Path $profile $entry.Key) -Raw) -eq $entry.Value) "Personal data changed: $($entry.Key)" }
            Check (!(Test-Path -LiteralPath ($target + '.cage-installation.json'))) 'A completed failure left a blocking recovery receipt.'
            Check (@(Get-ChildItem -LiteralPath (Join-Path $profile 'updates') -Directory -Filter 'previous-*').Count -eq 0) 'A finished transaction retained an unnecessary program backup.'
            if ($mode -eq 'installer-failure') {
                Check (!(Test-Path -LiteralPath (Join-Path $profile 'handoff-0.1.8.json'))) 'An untouched original was treated as an installed candidate.'
                Check (@(Get-ChildItem -LiteralPath $scenario -Directory -Filter '*.restore-*').Count -eq 0) 'Installer rejection created an unnecessary rollback tree.'
            }
            if ($mode -eq 'startup-failure') {
                $rollback = Wait-File (Join-Path $profile 'handoff-0.1.7.json')
                $backupPrefix = Join-Path $profile 'updates/previous-'
                Check ($rollback.stop -and $rollback.executable.StartsWith($backupPrefix, [StringComparison]::OrdinalIgnoreCase)) 'Rollback did not stop the environment through the backed-up original backend.'
                Check (!(Test-Path -LiteralPath (Join-Path $profile 'handoff-0.1.8.json'))) 'Rollback executed the failed candidate backend.'
            }
            $results += @{scenario = $mode; passed = $true; signatureVerification = $true; personalDataPreserved = $true}
            Write-Host "PASS: complete Windows update transaction ($mode)."
        } catch {
            $results += @{scenario = $mode; passed = $false; error = $_.Exception.Message}
            throw
        } finally {
            if ($disk) { $disk.Dispose() }
            Write-Text (Join-Path $profile 'release.json') '{}'
            foreach ($owned in @($parent, $helper, $holder)) {
                if (!$owned) { continue }
                try { if (!$owned.WaitForExit(10000)) { $owned.Kill($true); $owned.WaitForExit() } } finally { $owned.Dispose() }
            }
            foreach ($name in @('candidate','reopened')) {
                $record = Join-Path $profile ($name + '.json')
                if (!(Test-Path -LiteralPath $record)) { continue }
                $pidValue = (Get-Content -LiteralPath $record -Raw | ConvertFrom-Json).pid
                $owned = Get-Process -Id $pidValue -ErrorAction SilentlyContinue
                if ($owned) {
                    try {
                        if ($owned.MainModule.FileName -eq (Join-Path $target 'Cage.exe') -and !$owned.WaitForExit(10000)) { $owned.Kill(); $owned.WaitForExit() }
                    } finally { $owned.Dispose() }
                }
            }
            if (Test-Path $registration) {
                Check ((Get-ItemProperty $registration).InstallLocation -eq $target) 'Fixture registration changed owner; retained for inspection.'
                Remove-Item -LiteralPath $registration -Recurse
            }
        }
    }
    $passed = $true
} finally {
    @{success = $passed; scenarios = $results; transport = 'local fixture'; readiness = 'headless acknowledgement'} | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $reportPath
    if ($certificate) {
        foreach ($storeName in @('Root','TrustedPublisher')) { Remove-Item "Cert:\LocalMachine\$storeName\$($certificate.Thumbprint)" -Force -ErrorAction SilentlyContinue }
        Remove-Item "Cert:\CurrentUser\My\$($certificate.Thumbprint)" -DeleteKey -Force -ErrorAction SilentlyContinue
    }
    # All paths below were created by this invocation. Preserve a failed run.
    if ($passed) { Remove-Item -LiteralPath $root -Recurse -Force }
    else { Write-Warning "Update fixture evidence retained at $root" }
    Write-Host "Windows update report: $reportPath"
}
