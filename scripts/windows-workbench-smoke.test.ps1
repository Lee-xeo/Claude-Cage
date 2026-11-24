# Cross-platform regression for the real smoke-report and CI artifact contract.
. (Join-Path $PSScriptRoot 'windows-common.ps1')
$directory = Join-Path ([IO.Path]::GetTempPath()) ('cage-smoke-contract-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $directory | Out-Null
$savedRunnerTemp = $env:RUNNER_TEMP
function Assert-SmokeContract([bool]$Condition, [string]$Message) { if (-not $Condition) { throw $Message } }
try {
    $env:RUNNER_TEMP = Join-Path $directory 'runner-temp'
    Assert-SmokeContract ((Get-WindowsWorkbenchTestBase) -eq [IO.Path]::GetFullPath($env:RUNNER_TEMP)) 'CI report directory ignores RUNNER_TEMP.'
    Assert-SmokeContract ((Get-WindowsWorkbenchTestBase -WorkbenchProfile) -eq [IO.Path]::GetFullPath($env:RUNNER_TEMP)) 'CI workbench profile ignores RUNNER_TEMP.'
    $savedTemp = $env:TEMP; $savedTmp = $env:TMP
    $envFile = Join-Path $directory 'child-env.json'
    $envScript = Join-Path $directory 'child-env.ps1'
    [IO.File]::WriteAllText($envScript, '@{temp=$env:TEMP;tmp=$env:TMP} | ConvertTo-Json | Set-Content -LiteralPath $args[0]')
    $failed = $false
    try {
        Invoke-InIsolatedTestTemp {
            Invoke-BuildTool (Get-Process -Id $PID).Path @('-NoProfile','-NonInteractive','-File',$envScript,$envFile)
            throw 'injected test failure'
        }
    } catch { $failed = $_.Exception.Message -eq 'injected test failure' }
    $childEnv = Get-Content -LiteralPath $envFile -Raw | ConvertFrom-Json
    Assert-SmokeContract $failed 'Isolated test temp swallowed its test failure.'
    Assert-SmokeContract ($childEnv.temp -eq $childEnv.tmp -and (Split-Path $childEnv.temp -Parent) -eq $env:RUNNER_TEMP) 'Child tests did not inherit the isolated runner temp.'
    Assert-SmokeContract ($env:TEMP -eq $savedTemp -and $env:TMP -eq $savedTmp) 'Failed tests changed the caller temp environment.'
    Assert-SmokeContract (-not (Test-Path -LiteralPath $childEnv.temp)) 'Owned test temp was not cleaned.'
    Remove-Item Env:RUNNER_TEMP -ErrorAction SilentlyContinue
    Assert-SmokeContract ((Get-WindowsWorkbenchTestBase) -eq [IO.Path]::GetTempPath()) 'Local report directory lost its OS temp fallback.'
    $checkout = Split-Path $PSScriptRoot -Parent
    Push-Location $directory
    try {
        Assert-SmokeContract ((Get-WindowsWorkbenchTestBase -WorkbenchProfile) -eq (Join-Path $checkout '_output/workbench-smoke')) 'Local workbench profile must stay in the checkout output directory.'
    } finally { Pop-Location }
    if ($IsWindows -and $env:LOCALAPPDATA) {
        # An AppData checkout recreates the old timeout even with the new
        # checkout-relative default. No files need to be written there.
        $privateStage = Join-Path $env:LOCALAPPDATA 'Cage-test-checkout/_output/workbench-smoke'
        $rejected = $false
        try { Assert-WindowsWorkbenchTestBase $privateStage }
        catch { $rejected = $_.Exception.Message -match 'Set RUNNER_TEMP' }
        Assert-SmokeContract $rejected 'Smoke preflight accepted a checkout under AppData.'
    }
    $report = Join-Path $directory 'smoke.json'
    $fixture = Join-Path $directory 'fixture.json'
    $profile = Join-Path $directory 'profile'
    $storageError = '请选择具体项目，不要共享主目录、Cage 配置或包含配置的上级目录'
    $controllerLogs = Join-Path $profile 'logs'
    New-Item -ItemType Directory -Path $controllerLogs -Force | Out-Null
    @(
        '{unfinished'
        '{"level":"error","source":"command","message":"unrelated-private-controller-message"}'
        (@{ level = 'error'; source = 'command'; message = $storageError } | ConvertTo-Json -Compress)
    ) | Set-Content -LiteralPath (Join-Path $controllerLogs 'events.jsonl')
    $childScript = Join-Path $directory 'child.ps1'
    [IO.File]::WriteAllText($childScript, '[IO.File]::Copy($args[0],$args[1],$true); exit 17')
    @{
        success = $false; nativeBridgeReady = $false; phase = 'loading_workbench'
        prepareExit = 1; prepareStatus = 'stop_required'
        prepareMessage = 'shutdown failed http://localhost:1234/api/handoff?token=private-token#private-fragment'
        nativeStatus = 'failed http://localhost:1234/?token=private-token#private-fragment'
        entryJSON = 'private-entry'; pageURL = 'http://localhost:1234/#private-launch'
        page = @{ text = 'private-page-body'; errors = @(
            @{ type = 'module-load'; source = '/missing.js?session=private-query#private-fragment' },
            @{ type = 'error'; source = '/main.js'; message = 'syntax error'; stack = @('at http://localhost:1234/main.js?session=private-query#private-fragment') }
        ) }
    } | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath $fixture
    $start = [Diagnostics.ProcessStartInfo]::new((Get-Process -Id $PID).Path)
    $start.UseShellExecute = $false
    foreach ($argument in @('-NoProfile','-NonInteractive','-File',$childScript,$fixture,$report)) { $start.ArgumentList.Add($argument) }
    $child = [Diagnostics.Process]::Start($start)
    try {
        $child.WaitForExit()
        Assert-SmokeContract ($child.ExitCode -eq 17) 'Failure fixture did not exit unsuccessfully.'
        $failed = $false
        $captured = & {
            try { Assert-WindowsWorkbenchReport -ExitCode $child.ExitCode -Report $report -ControllerProfile $profile }
            catch { $script:observedFailure = $_.Exception.Message; Write-Output ('Rejected smoke: ' + $_.Exception.Message) }
        } 6>&1
        $text = ($captured | ForEach-Object { $_.ToString() }) -join "`n"
        Assert-SmokeContract ($observedFailure -like '*loading_workbench*') 'Failure report was not parsed before rejecting its process exit.'
        foreach ($expected in @('loading_workbench', 'module-load', '/missing.js', 'syntax error', '"prepareExit":1', '"prepareStatus":"stop_required"', 'shutdown failed http://localhost:1234/api/handoff', ('"controllerError":"' + $storageError + '"'))) {
            Assert-SmokeContract ($text.Contains($expected)) "Missing safe smoke evidence: $expected"
        }
        foreach ($private in @('private-token','private-fragment','private-query','private-entry','private-launch','private-page-body','unrelated-private-controller-message')) {
            Assert-SmokeContract (-not $text.Contains($private)) "Private smoke field leaked: $private"
        }
    } finally { $child.Dispose() }
    $unusableReport = Join-Path $directory 'unusable.json'
    $unusableReports = [ordered]@{
        'No smoke report' = $null
        'Invalid smoke report' = '{"unrelated-private-controller-message":'
        'exceeds the bounded diagnostic size' = ('x' * 65537)
    }
    foreach ($reason in $unusableReports.Keys) {
        if ($null -ne $unusableReports[$reason]) { [IO.File]::WriteAllText($unusableReport, $unusableReports[$reason]) }
        $failure = ''
        try { Assert-WindowsWorkbenchReport -ExitCode 17 -Report $unusableReport -ControllerProfile $profile }
        catch { $failure = $_.Exception.Message }
        Assert-SmokeContract ($failure.Contains($reason) -and $failure.Contains($storageError)) "Missing backend evidence for $reason."
        Assert-SmokeContract (-not $failure.Contains('unrelated-private-controller-message')) 'Unsafe controller or report content leaked.'
    }
    $timeoutFailure = ''
    try { Stop-WindowsWorkbenchAcceptance 'Cage workbench acceptance timed out.' $profile }
    catch { $timeoutFailure = $_.Exception.Message }
    Assert-SmokeContract ($timeoutFailure.Contains('timed out') -and $timeoutFailure.Contains($storageError)) 'Missing backend evidence on timeout.'
    [IO.File]::WriteAllText($report, '{"success":true,"nativeBridgeReady":true}')
    Assert-WindowsWorkbenchReport -ExitCode 0 -Report $report
    $rejected = $false
    try { Assert-WindowsWorkbenchReport -ExitCode 17 -Report $report } catch { $rejected = $true }
    Assert-SmokeContract $rejected 'Nonzero child exit was accepted because its report said success.'
    $app = Join-Path $directory 'app'
    New-Item -ItemType Directory -Path (Join-Path $app 'resources') -Force | Out-Null
    [IO.File]::WriteAllText((Join-Path $app 'Cage.exe'), 'native fixture')
    $image = Join-Path $app 'resources/cage.exe'
    [IO.File]::WriteAllText($image, 'backend fixture')
    $ready = Join-Path $directory 'image-locked'
    $released = Join-Path $directory 'image-released'
    $lockScript = Join-Path $directory 'lock-image.ps1'
    [IO.File]::WriteAllText($lockScript, @'
$handle = [IO.File]::Open($args[0], [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
try {
    [IO.File]::WriteAllText($args[1], 'ready')
    Start-Sleep -Milliseconds 800
} finally {
    [IO.File]::WriteAllText($args[2], 'released')
    $handle.Dispose()
}
'@)
    $start = [Diagnostics.ProcessStartInfo]::new((Get-Process -Id $PID).Path)
    $start.UseShellExecute = $false
    foreach ($argument in @('-NoProfile','-NonInteractive','-File',$lockScript,$image,$ready,$released)) { $start.ArgumentList.Add($argument) }
    $child = [Diagnostics.Process]::Start($start)
    try {
        $deadline = [DateTime]::UtcNow.AddSeconds(10)
        while (-not (Test-Path -LiteralPath $ready)) {
            if ($child.HasExited -or [DateTime]::UtcNow -ge $deadline) { throw 'Image lock fixture did not start.' }
            Start-Sleep -Milliseconds 20
        }
        Wait-WindowsWorkbenchFiles -AppDirectory $app
        Assert-SmokeContract (Test-Path -LiteralPath $released) 'Smoke returned while a child still held its executable image.'
        $child.WaitForExit()
        Assert-SmokeContract ($child.ExitCode -eq 0) 'Image lock fixture failed.'
    } finally {
        if (-not $child.HasExited) { $child.Kill($true); $child.WaitForExit() }
        $child.Dispose()
    }
    Assert-SmokeContract (Test-WindowsLowIntegrityText @('C:\sandbox\repo Mandatory Label\Low Mandatory Level:(OI)(CI)(NW)')) 'English icacls Low Mandatory Level was not detected.'
    Assert-SmokeContract (Test-WindowsLowIntegrityText @('C:\sandbox\repo Mandatory Label\低强制级别:(OI)(CI)(NW)')) 'Chinese icacls Low Mandatory Level was not detected.'
    Assert-SmokeContract (Test-WindowsLowIntegrityText @('Mandatory Label\Low Mandatory Level Label S-1-16-4096 Mandatory group')) 'whoami /groups S-1-16-4096 SID was not detected.'
    Assert-SmokeContract (Test-WindowsLowIntegrityText @('S:(ML;OICI;NW;;;LW)')) 'SDDL Low Mandatory Level was not detected.'
    Assert-SmokeContract (-not (Test-WindowsLowIntegrityText @(
        'C:\src\Claude-Cage Mandatory Label\Medium Mandatory Level:(OI)(CI)(NW)',
        'Mandatory Label\Medium Mandatory Level Label S-1-16-8192 Mandatory group',
        'Mandatory Label\High Mandatory Level Label S-1-16-12288 Mandatory group'
    ))) 'Medium or High integrity was falsely flagged as Low Mandatory Level.'
    Assert-WindowsBuildDirectoryIntegrity $directory
    Write-Output 'PASS: isolated temp, low-IL preflight, safe smoke diagnostics, exit status and executable release.'
} finally {
    $env:RUNNER_TEMP = $savedRunnerTemp
    Remove-Item -LiteralPath $directory -Recurse
}
