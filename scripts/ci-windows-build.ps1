#requires -Version 7.0
# CI orchestration only. Builds the checked-out version and never publishes it.
[CmdletBinding()]
param([switch]$Release)
. (Join-Path $PSScriptRoot 'windows-common.ps1')
if (-not $IsWindows) { throw 'This CI entry point requires a disposable Windows runner.' }
if ($env:CAGE_BUILD_REF -notmatch '^[0-9A-Fa-f]{40}$') { throw 'CAGE_BUILD_REF must be a complete commit SHA.' }
$repository = Split-Path $PSScriptRoot -Parent
Push-Location $repository
$pfx = $null
$certificate = $null
$certificates = @()
$qemuSource = $null
$savedPath = $env:PATH
$savedCertificate = $env:CAGE_WINDOWS_CERT_SHA1
$savedStorageVolume = $env:CAGE_TEST_STORAGE_VOLUME
$storageTestRoot = $null
# One step runs every phase, because signing state must not outlive this
# process. Name each phase so a failure shows where it happened and how long
# the earlier phases took, without reading the whole log.
$script:phase = $null
$script:phaseStarted = $null
$script:phases = [System.Collections.Generic.List[object]]::new()
function Complete-CiPhase {
    param([string]$Result = 'passed')
    if (-not $script:phase) { return }
    $seconds = [int]([DateTime]::UtcNow - $script:phaseStarted).TotalSeconds
    $script:phases.Add([pscustomobject]@{ Name = $script:phase; Seconds = $seconds; Result = $Result })
    if ($env:GITHUB_ACTIONS) { Write-Host '::endgroup::' }
    Write-Host "Phase $Result after ${seconds}s: $($script:phase)"
    $script:phase = $null
}
function Enter-CiPhase {
    param([Parameter(Mandatory)][string]$Name)
    Complete-CiPhase
    $script:phase = $Name
    $script:phaseStarted = [DateTime]::UtcNow
    if ($env:GITHUB_ACTIONS) { Write-Host "::group::$Name" } else { Write-Host "== $Name" }
}
function Write-CiPhaseSummary {
    if (-not $env:GITHUB_STEP_SUMMARY -or $script:phases.Count -eq 0) { return }
    $lines = @('| Windows phase | Seconds | Result |', '| --- | ---: | --- |')
    $lines += $script:phases | ForEach-Object { "| $($_.Name) | $($_.Seconds) | $($_.Result) |" }
    $lines + '' | Out-File -FilePath $env:GITHUB_STEP_SUMMARY -Append -Encoding utf8
}
try {
    Enter-CiPhase 'Checkout identity and script tests'
    $actual = (& git rev-parse HEAD).Trim()
    if ($LASTEXITCODE -ne 0 -or $actual -ne $env:CAGE_BUILD_REF) { throw 'Checkout differs from the requested commit.' }
    $windowsPowerShell = Join-Path ([Environment]::SystemDirectory) 'WindowsPowerShell/v1.0/powershell.exe'
    # These tests read only the checkout. Run them before the long build so
    # a broken script fails in the first minute instead of after packaging.
    $scriptTests = @('windows-workbench-packages.tests.ps1', 'claude-desktop-windows.tests.ps1', 'windows-claude-cli.tests.ps1',
        'windows-host-enrollment.tests.ps1', 'windows-iso-setup.tests.ps1', 'windows-host-image.tests.ps1', 'new-windows-test-vm.tests.ps1')
    if (-not $Release) { $scriptTests += @('install-guest-powershell.tests.ps1', 'windows-evaluation.tests.ps1') }
    foreach ($test in $scriptTests) {
        Invoke-BuildTool $windowsPowerShell @('-NoProfile', '-NonInteractive', '-File', "scripts/guest-os/$test")
    }
    Invoke-BuildTool 'node' @('scripts/check-version.mjs')
    Invoke-BuildTool 'uv' @('run', '--no-project', '--python', '3.13', 'internal/workbench/cache_maintenance_test.py')
    Invoke-BuildTool 'node' @('--test', 'internal/cage/browser-clipboard.test.mjs', 'scripts/windows-native-bridge.test.mjs')
    if ($Release) {
        Enter-CiPhase 'Release signing configuration'
        if ($env:GITHUB_EVENT_NAME -ne 'workflow_dispatch') { throw 'Release candidates require an explicitly dispatched release workflow.' }
        if ($env:CAGE_WINDOWS_SIGNING_PROVIDER -eq 'esigner') {
            foreach ($name in @('CAGE_ESIGNER_USERNAME', 'CAGE_ESIGNER_PASSWORD', 'CAGE_ESIGNER_CREDENTIAL_ID', 'CAGE_ESIGNER_CERTIFICATE_SERIAL', 'CAGE_ESIGNER_TOTP_SECRET')) {
                if (-not [Environment]::GetEnvironmentVariable($name)) { throw "Missing cage-release setting: $name" }
            }
            Assert-WindowsSigningConfiguration
            Invoke-BuildTool 'node' @('scripts/esigner.mjs', 'install')
        } elseif ($env:CAGE_WINDOWS_SIGNING_PROVIDER -and $env:CAGE_WINDOWS_SIGNING_PROVIDER -ne 'certificate-store') {
            throw 'Unknown Windows signing provider.'
        } elseif (-not $env:CAGE_WINDOWS_PFX_BASE64 -or -not $env:CAGE_WINDOWS_PFX_PASSWORD) {
            throw 'Configure CAGE_WINDOWS_PFX_BASE64 and CAGE_WINDOWS_PFX_PASSWORD in the cage-release environment.'
        }
        if ($env:CAGE_WINDOWS_SIGNING_PROVIDER -ne 'esigner') {
            $pfx = Join-Path $env:RUNNER_TEMP ('cage-signing-' + [guid]::NewGuid().ToString('N') + '.pfx')
            [IO.File]::WriteAllBytes($pfx, [Convert]::FromBase64String($env:CAGE_WINDOWS_PFX_BASE64))
            $password = ConvertTo-SecureString $env:CAGE_WINDOWS_PFX_PASSWORD -AsPlainText -Force
            $certificates = @(Import-PfxCertificate -FilePath $pfx -CertStoreLocation Cert:\CurrentUser\My -Password $password)
            Remove-Item -LiteralPath $pfx -Force
            $signers = @($certificates | Where-Object {
                $_.HasPrivateKey -and @($_.EnhancedKeyUsageList | Where-Object { $_.ObjectId -eq '1.3.6.1.5.5.7.3.3' }).Count -gt 0
            })
            if ($signers.Count -ne 1) { throw 'PFX must contain exactly one code-signing certificate with its private key.' }
            $certificate = $signers[0]
            $env:CAGE_WINDOWS_CERT_SHA1 = $certificate.Thumbprint
            Remove-Item Env:CAGE_WINDOWS_PFX_BASE64, Env:CAGE_WINDOWS_PFX_PASSWORD
        }
    }

    Enter-CiPhase 'Installer compiler and installation helper tests'
    & (Join-Path $PSScriptRoot 'install-nsis.ps1') -Destination $env:RUNNER_TEMP
    $env:PATH = (Join-Path $env:RUNNER_TEMP 'nsis-3.13') + [IO.Path]::PathSeparator + $env:PATH
    Invoke-BuildTool (Get-BuildCommand @('pwsh.exe', 'pwsh')) @('-NoProfile', '-NonInteractive', '-File', 'scripts/windows-workbench-smoke.test.ps1')
    # NSIS is x86 and resolves $SYSDIR to SysWOW64. Match its real helper host.
    $installationPowerShell = Join-Path $env:SystemRoot 'SysWOW64/WindowsPowerShell/v1.0/powershell.exe'
    & (Join-Path $PSScriptRoot 'test-windows-installation-files.ps1') -HelperShell $installationPowerShell -InstallerSmoke
    if ($Release) {
        Enter-CiPhase 'Corresponding sources'
        & (Join-Path $PSScriptRoot 'prepare-qemu-release.ps1')
        $qemuSource = Join-Path $repository '_output/windows-qemu-sources/full-source.tar.gz'
    }
    Enter-CiPhase $(if ($Release) { 'Build and sign the application' } else { 'Build the application' })
    & (Join-Path $PSScriptRoot 'build-windows.ps1') -Release:$Release
    Enter-CiPhase 'Go, PowerShell media and desktop shell tests'
    Invoke-BuildTool 'go' @('test', './internal/guestos', '-run', '^TestWindows(HostEnrollment|UserToolACL)', '-count=1')
    Invoke-BuildTool 'go' @('test', './internal/hostproxy', './internal/guestos', './cmd/cage', '-run', '^Test(ParallelPackage|DownloadLockCancellationAndReuse|WindowsPackageDownloadUsesBundledHostDownloader|HostDownloadParentExitCancels)', '-count=1')
    if (-not $Release) {
        Invoke-BuildTool $windowsPowerShell @('-NoProfile', '-NonInteractive', '-File', 'scripts/guest-os/windows-powershell-media.smoke.ps1',
            '-GuestToolsDirectory', "$repository/_output/windows-amd64/app/resources/guest-os")
    }
    Invoke-BuildTool 'dotnet' @('run', '--project', 'desktop/windows.tests/Cage.Windows.Tests.csproj')
    # RUNNER_TEMP and the checkout share D: on hosted runners. Use a
    # unique system-drive directory, outside user data, for the other side.
    if (-not $env:CAGE_TEST_STORAGE_VOLUME) {
        $systemDrive = [IO.Path]::GetPathRoot($env:SystemRoot)
        $testDrive = [IO.Path]::GetPathRoot((Get-WindowsWorkbenchTestBase))
        if ($systemDrive -ne $testDrive) {
            $storageTestRoot = (New-Item -ItemType Directory -Path (Join-Path $systemDrive ('cage-storage-volume-' + [guid]::NewGuid().ToString('N')))).FullName
            $env:CAGE_TEST_STORAGE_VOLUME = $storageTestRoot
            Write-Host "Storage migration tests use separate drives: $testDrive -> $systemDrive"
        } else {
            Write-Warning 'No second test volume is configured; the cross-volume storage test will be skipped.'
        }
    }
    Invoke-InIsolatedTestTemp {
        Invoke-BuildTool 'go' @('test', './internal/workbench', './internal/appupdate', './internal/host', './internal/managedproxy', './internal/env', './internal/paths', './internal/mounts')
        Invoke-BuildTool 'go' @('test', './internal/cage', '-run', 'Test(Storage|Gate|ProxyRevision|ConfigChange|JobStop|CONNECT|SOCKS|DNS|RejectHostLAN|Credentials|ProxyHostname|SafetyReport|Renew)')
        Invoke-BuildTool 'go' @('test', './internal/vm', '-run', 'Test(NetworkConfined|WindowsQEMUIdentity)')
        Invoke-BuildTool 'go' @('test', 'github.com/lima-vm/lima/v2/pkg/driver/qemu', '-run', 'Test(WindowsFirmwareArgument|WindowsPackagedFirmwareBeatsOldUserCopy|WHPXQEMU64Features|WindowsCPUResolution)$')
        Invoke-BuildTool 'go' @('test', 'github.com/lima-vm/lima/v2/pkg/hostagent', '-run', '^TestResourceProbeCommand$')
        Invoke-BuildTool 'go' @('test', 'github.com/lima-vm/lima/v2/pkg/networks/usernet', '-run', 'TestNetstackWindowsNativeSocketPaths')
        Invoke-BuildTool 'go' @('vet', './cmd/cage', './internal/cage', './internal/workbench', './internal/appupdate', './internal/host')
    }
    Enter-CiPhase 'Package the installer'
    $packageArguments = @{ Release = $Release; SkipBuild = $true }
    if ($Release) { $packageArguments.QemuSourceArchive = $qemuSource }
    & (Join-Path $PSScriptRoot 'package-windows.ps1') @packageArguments
    $version = Get-CageVersion $repository
    $suffix = if ($Release) { '' } else { '-unsigned' }
    Enter-CiPhase 'Install, update and uninstall with isolated data'
    & (Join-Path $PSScriptRoot 'test-windows-install.ps1') -Installer "$repository/_output/releases/$version/windows/Cage-$version-windows-amd64-setup$suffix.exe"
    if ($Release) {
        Enter-CiPhase 'Signed automatic update and rollback transactions'
        & (Join-Path $PSScriptRoot 'test-windows-update-flow.ps1')
        Invoke-BuildTool 'node' @('scripts/release-version.mjs', 'metadata', 'windows', "_output/releases/$version/windows/build-metadata.json")
    }
    Complete-CiPhase
} catch {
    $failedPhase = $script:phase
    Complete-CiPhase 'failed'
    if ($env:GITHUB_ACTIONS -and $failedPhase) {
        # Workflow commands are single-line; the full error follows below.
        $message = ("$($_.Exception.Message)" -replace '\r?\n', ' ')
        Write-Host "::error title=Windows phase failed: ${failedPhase}::${message}"
    }
    throw
} finally {
    Write-CiPhaseSummary
    if ($pfx) { Remove-Item -LiteralPath $pfx -Force -ErrorAction SilentlyContinue }
    foreach ($imported in $certificates) {
        if ($imported.HasPrivateKey) {
            Remove-Item "Cert:\CurrentUser\My\$($imported.Thumbprint)" -DeleteKey -Force -ErrorAction SilentlyContinue
        } else {
            Remove-Item "Cert:\CurrentUser\My\$($imported.Thumbprint)" -Force -ErrorAction SilentlyContinue
        }
    }
    # Verified QEMU sources remain in the build cache; packaging copies them separately.
    $env:PATH = $savedPath
    $env:CAGE_WINDOWS_CERT_SHA1 = $savedCertificate
    $env:CAGE_TEST_STORAGE_VOLUME = $savedStorageVolume
    if ($storageTestRoot) { Remove-Item -LiteralPath $storageTestRoot -Recurse -Force -ErrorAction SilentlyContinue }
    Remove-Item Env:CAGE_ESIGNER_PASSWORD, Env:CAGE_ESIGNER_TOTP_SECRET -ErrorAction SilentlyContinue
    Pop-Location
}
