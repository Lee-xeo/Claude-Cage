#requires -Version 7.0
[CmdletBinding()]
param(
    [string]$DependencyCache,
    [string]$OutputDirectory,
    [switch]$UseFingerprintCache,
    [string]$MsysDirectory = $env:CAGE_MSYS2_ROOT
)
. (Join-Path $PSScriptRoot 'windows-common.ps1')
if (-not $IsWindows) { throw 'Build the Windows QEMU runtime in an MSYS2 UCRT64 environment on Windows.' }
$repository = Split-Path $PSScriptRoot -Parent
if (-not $DependencyCache) { $DependencyCache = Join-Path $repository '_output/windows-deps' }
if ($UseFingerprintCache -and $OutputDirectory) { throw 'UseFingerprintCache selects its own QEMU output directory.' }
$DependencyCache = [IO.Path]::GetFullPath($DependencyCache)
$dependencies = Get-Content -LiteralPath (Join-Path $repository 'packaging/windows/dependencies.json') -Raw | ConvertFrom-Json
$patchDirectory = Join-Path $repository 'patches/qemu'
$inputs = @((Join-Path $PSScriptRoot 'build-qemu-windows.sh'), $PSCommandPath) + @(Get-ChildItem -LiteralPath $patchDirectory -Filter '*.patch' | Sort-Object Name | ForEach-Object FullName)
$fingerprintText = $dependencies.qemuSource.hash + $dependencies.qemuKeycodemap.hash + (($inputs | ForEach-Object { (Get-FileHash -LiteralPath $_ -Algorithm SHA256).Hash }) -join '')
$fingerprintText += ConvertTo-Json -Compress -InputObject @($env:CC, $env:CXX)
$fingerprint = [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData([Text.Encoding]::UTF8.GetBytes($fingerprintText)))

function Build-WindowsQemuRuntime {
    if (-not $MsysDirectory) {
        $MsysDirectory = @('C:\msys64', 'D:\msys64') | Where-Object { Test-Path (Join-Path $_ 'ucrt64/bin/gcc.exe') } | Select-Object -First 1
    }
    if (-not $MsysDirectory) {
        throw 'Set CAGE_MSYS2_ROOT to an MSYS2 installation with the UCRT64 packages listed in docs/windows.md#windows-源码构建.'
    }
    $MsysDirectory = [IO.Path]::GetFullPath($MsysDirectory)
    $shortPath = '^[A-Za-z]:[\\/][A-Za-z0-9_.\\/-]*$'
    if ($MsysDirectory -notmatch $shortPath) {
        throw 'CAGE_MSYS2_ROOT must be a short local path with ASCII characters and no spaces (for example C:\msys64).'
    }
    $required = @('usr/bin/bash.exe', 'usr/bin/cygpath.exe', 'usr/bin/tar.exe', 'usr/bin/gzip.exe',
        'usr/bin/make.exe', 'usr/bin/patch.exe', 'usr/bin/git.exe', 'usr/bin/diff.exe', 'usr/bin/pacman.exe',
        'ucrt64/bin/gcc.exe', 'ucrt64/bin/g++.exe', 'ucrt64/bin/as.exe', 'ucrt64/bin/ld.exe',
        'ucrt64/bin/ninja.exe', 'ucrt64/bin/python.exe', 'ucrt64/bin/pkg-config.exe', 'ucrt64/bin/strip.exe', 'ucrt64/bin/objdump.exe')
    $missing = @($required | Where-Object { -not (Test-Path -LiteralPath (Join-Path $MsysDirectory $_) -PathType Leaf) })
    if ($missing.Count) { throw "Missing MSYS2 tools: $($missing -join ', '). Install the UCRT64 packages listed in docs/windows.md#windows-源码构建." }

    # A system-wide MSYS2 installation may have an unwritable /tmp. All QEMU
    # paths and native compiler scratch files must stay under a usable short path.
    $roots = if ($env:CAGE_QEMU_BUILD_ROOT) { @($env:CAGE_QEMU_BUILD_ROOT) } else {
        @((Join-Path $MsysDirectory 'tmp'), $env:RUNNER_TEMP, [IO.Path]::GetTempPath())
    }
    $stage = $null
    foreach ($root in $roots) {
        if (-not $root) { continue }
        $root = [IO.Path]::GetFullPath($root)
        if ($root -notmatch $shortPath) { continue }
        $candidate = Join-Path $root ('cage-qemu-' + [guid]::NewGuid().ToString('N').Substring(0, 12))
        try { $stage = (New-Item -Path $candidate -ItemType Directory -Force -ErrorAction Stop).FullName; break }
        catch [UnauthorizedAccessException], [IO.IOException] { continue }
    }
    # QEMU configure installs Python packages through pip, whose nested wheel
    # cache paths below this directory can exceed MAX_PATH. SUBST drives do not
    # help because Python expands them to the physical path.
    if ($stage -and $stage.Length -gt 48) {
        Write-Warning "QEMU build directory $stage is long; pip may fail with 'The filename or extension is too long'. Set CAGE_QEMU_BUILD_ROOT to a short physical path such as C:\CageBuildTemp."
    }
    if (-not $stage) {
        throw 'QEMU needs a writable, short local build path with ASCII characters and no spaces. Create one and set CAGE_QEMU_BUILD_ROOT to it.'
    }
    $built = Join-Path $outputParent ('.cage-qemu-' + [guid]::NewGuid().ToString('N'))
    $savedSystem, $savedPath = $env:MSYSTEM, $env:PATH
    $savedTemp, $savedTmp, $savedTmpDir = $env:TEMP, $env:TMP, $env:TMPDIR
    $complete = $false
    try {
        $uclib = Join-Path $MsysDirectory 'ucrt64/bin'
        $msysbin = Join-Path $MsysDirectory 'usr/bin'
        $bash = Join-Path $msysbin 'bash.exe'
        $env:PATH = "$uclib;$msysbin;$savedPath"
        $env:MSYSTEM = 'UCRT64'
        # Use the selected MSYS2 tools without running login scripts that can
        # change the working directory or PATH on a contributor's machine.
        $shell = @('--noprofile', '--norc', (Join-Path $PSScriptRoot 'build-qemu-windows.sh'))
        try { Invoke-BuildTool (Join-Path $uclib 'pkg-config.exe') @('--print-errors', '--exists', 'glib-2.0', 'pixman-1', 'slirp') }
        catch { throw 'Missing QEMU libraries in MSYS2 UCRT64. Install glib2, pixman and libslirp from the packages listed in docs/windows.md#windows-源码构建.' }
        Invoke-BuildTool $bash ($shell + @('preflight', $stage)) | Out-Host
        $archive = Get-VerifiedGitArchive $dependencies.qemuSource $DependencyCache
        $keymapArchive = Get-VerifiedDependency $dependencies.qemuKeycodemap $DependencyCache
        Invoke-BuildTool $bash ($shell + @('configure', $stage, $archive, $keymapArchive, $patchDirectory)) | Out-Host
        # Start native Ninja directly. Its child MSYS tools can initialize their
        # own runtime rather than inheriting Bash's MSYS process namespace.
        $env:TEMP = $env:TMP = $env:TMPDIR = Join-Path $stage 'tmp'
        Invoke-BuildTool (Join-Path $uclib 'ninja.exe') @('-C', (Join-Path $stage 'build'), '-j', '6', 'qemu-system-x86_64.exe', 'qemu-img.exe') | Out-Host
        Invoke-BuildTool $bash ($shell + @('package', $stage, $built, ([Environment]::SystemDirectory))) | Out-Host
        Assert-WindowsQemuRuntime $built
        $files = @(Get-ChildItem -LiteralPath $built -File | Sort-Object Name | ForEach-Object {
            [ordered]@{ name = $_.Name; sha256 = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash }
        })
        [ordered]@{ fingerprint = $fingerprint; source = $dependencies.qemuSource.commit; files = $files } |
            ConvertTo-Json -Depth 4 | Set-Content -LiteralPath (Join-Path $built 'cage-build.json')
        if (-not (Test-WindowsQemuBuildManifest $built $fingerprint)) { throw 'The staged QEMU runtime fails manifest verification.' }
        Publish-WindowsQemuRuntime -StagedDirectory $built -Directory $OutputDirectory -ManagedCache:$managedOutput
        $complete = $true
    } finally {
        $env:MSYSTEM = $savedSystem
        $env:PATH = $savedPath
        $env:TEMP = $savedTemp; $env:TMP = $savedTmp; $env:TMPDIR = $savedTmpDir
        if (Test-Path -LiteralPath $built) { Remove-Item -LiteralPath $built -Recurse -Force -ErrorAction SilentlyContinue }
        if ($complete) { Remove-Item -LiteralPath $stage -Recurse -Force -ErrorAction SilentlyContinue }
        else { Write-Warning "QEMU build files retained at $stage" }
    }
    Write-Host "Built Windows QEMU runtime at $OutputDirectory"
}

$defaultOutput = Join-Path $DependencyCache 'qemu-cage'
if ($UseFingerprintCache) {
    # Consumers can use this exact runtime while another configuration builds.
    $OutputDirectory = Join-Path $DependencyCache "qemu-runtimes/$fingerprint"
} elseif (-not $OutputDirectory) {
    $OutputDirectory = $defaultOutput
}
$OutputDirectory = [IO.Path]::TrimEndingDirectorySeparator([IO.Path]::GetFullPath($OutputDirectory))
$managedOutput = $UseFingerprintCache -or $OutputDirectory -eq $defaultOutput
$outputParent = Split-Path -Parent $OutputDirectory
if (-not $outputParent) { throw 'Choose a dedicated QEMU output directory, not a volume root.' }
[void][IO.Directory]::CreateDirectory($outputParent)
$cacheLock = Enter-WindowsQemuOutputLock $OutputDirectory
try {
    if ((Test-Path -LiteralPath $OutputDirectory) -and (Test-WindowsQemuBuildManifest $OutputDirectory $fingerprint)) {
        Assert-WindowsQemuRuntime $OutputDirectory
        Write-Host "Verified Windows QEMU runtime at $OutputDirectory"
    } else {
        if (Test-Path -LiteralPath $OutputDirectory) { Write-Host "Rebuilding QEMU runtime at $OutputDirectory" }
        Build-WindowsQemuRuntime
    }
} finally {
    $cacheLock.Dispose()
}
if ($UseFingerprintCache) { Write-Output $OutputDirectory }
