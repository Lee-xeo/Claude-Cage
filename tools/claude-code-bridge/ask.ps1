[CmdletBinding(DefaultParameterSetName='Ask')]
param(
    [Parameter(Mandatory, Position=0, ParameterSetName='Ask')][string]$Text,
    [Parameter(ParameterSetName='Ask')][string]$SessionId,
    [Parameter(ParameterSetName='Ask')][switch]$NewSession,
    [Parameter(ParameterSetName='Ask')][string[]]$Files,
    [Parameter(Mandatory, ParameterSetName='Result')][string]$JobId,
    [ValidateRange(0,60)][int]$WaitSeconds = 30,
    [string]$HostId = $env:CAGE_HOST_ID
)
if (!$HostId) { throw 'Please set -HostId or CAGE_HOST_ID' }
$OutputEncoding = [Text.UTF8Encoding]::new($false)
if ($PSCmdlet.ParameterSetName -eq 'Result') {
    $tool = 'ask_result'
    $request = @{jobId=$JobId; waitSeconds=$WaitSeconds}
} else {
    $tool = 'ask'
    $request = @{text=$Text; waitSeconds=$WaitSeconds}
    if ($SessionId) { $request.sessionId = $SessionId }
    if ($NewSession) { $request.newSession = $true }
    if ($Files) { $request.files = $Files }
}
$request | ConvertTo-Json -Compress | node "$PSScriptRoot/server.mjs" --host-id $HostId --call $tool
exit $LASTEXITCODE
