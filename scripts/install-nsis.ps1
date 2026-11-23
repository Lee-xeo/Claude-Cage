#requires -Version 7.0
[CmdletBinding()]
param([string]$Destination = $env:RUNNER_TEMP)

$ErrorActionPreference = 'Stop'
if (-not $Destination) { throw 'Set RUNNER_TEMP or pass -Destination for the NSIS tool directory.' }
$version = '3.13'
$expectedSHA256 = 'ba63dffc4410ee89193e1cb5a41989991bd77c61068da17e3156d136b7b0b3d8'
$url = "https://downloads.sourceforge.net/project/nsis/NSIS%203/$version/nsis-$version.zip"
New-Item -ItemType Directory -Path $Destination -Force | Out-Null
$zip = Join-Path $Destination "nsis-$version.zip"

# CI keeps the ZIP between runs. Reuse it only when it still matches the
# pinned checksum; anything else is downloaded again and verified below.
$cached = (Test-Path -LiteralPath $zip -PathType Leaf) -and (Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash -eq $expectedSHA256
if (-not $cached) {
    # SourceForge returns a browser download page (HTTP 200 HTML) for PowerShell's
    # default user agent. Use the command-line downloader it redirects to the ZIP,
    # and fall back to Invoke-WebRequest with a curl user-agent on Windows when
    # Schannel revocation checks fail offline.
    $curl = if ($IsWindows) { 'curl.exe' } else { 'curl' }
    $partial = "$zip.partial-$([guid]::NewGuid().ToString('N'))"
    try {
        & $curl --fail --location --silent --show-error --retry 5 --retry-delay 5 --retry-all-errors --connect-timeout 15 --max-time 120 --output $partial --write-out "NSIS download: HTTP %{http_code}, %{content_type}, %{size_download} bytes\n" $url
        if ($LASTEXITCODE -ne 0) {
            if (-not $IsWindows) { throw "NSIS download failed (curl exit $LASTEXITCODE)." }
            Write-Warning "NSIS curl download failed (exit $LASTEXITCODE); falling back to Invoke-WebRequest with HTTPS and SHA-256 verification."
            Remove-Item -LiteralPath $partial -Force -ErrorAction SilentlyContinue
            for ($attempt = 1; ; $attempt++) {
                try {
                    Invoke-WebRequest -Uri $url -UserAgent 'curl/8.18.0' -OutFile $partial -TimeoutSec 120 -MaximumRedirection 5
                    break
                } catch {
                    if ($attempt -ge 4) { throw }
                    Write-Warning "NSIS Invoke-WebRequest failed (attempt $attempt): $($_.Exception.Message)"
                    Start-Sleep -Seconds (5 * $attempt)
                }
            }
        }
        Move-Item -LiteralPath $partial -Destination $zip -Force
    } finally {
        if (Test-Path -LiteralPath $partial) { Remove-Item -LiteralPath $partial -Force -ErrorAction SilentlyContinue }
    }
}

$stream = [IO.File]::OpenRead($zip)
try {
    $magic = [byte[]]::new(4)
    $read = $stream.Read($magic, 0, 4)
} finally { $stream.Dispose() }
if ($read -ne 4 -or [Convert]::ToHexString($magic) -ne '504B0304') {
    throw 'NSIS download is not a ZIP. SourceForge may have returned an HTML page; the downloaded content will not be installed.'
}
$actualSHA256 = (Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash
if ($actualSHA256 -ne $expectedSHA256) {
    throw "NSIS checksum mismatch. Expected $expectedSHA256; received $actualSHA256."
}
Expand-Archive -LiteralPath $zip -DestinationPath $Destination -Force
$toolDirectory = Join-Path $Destination "nsis-$version"
if (-not (Test-Path -LiteralPath (Join-Path $toolDirectory 'makensis.exe') -PathType Leaf)) {
    throw 'Verified NSIS ZIP did not contain the expected compiler.'
}
if ($env:GITHUB_PATH) { Add-Content -LiteralPath $env:GITHUB_PATH -Value $toolDirectory }
Write-Host "Verified NSIS $version ZIP and extracted the compiler."
