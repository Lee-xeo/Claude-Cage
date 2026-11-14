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
    Invoke-BuildTool 'uv' @('run', '--no-project', '--python', '3.13', 'internal/workbench/cache_maintenance_test.py')
