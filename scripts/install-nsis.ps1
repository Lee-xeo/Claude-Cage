#requires -Version 7.0
[CmdletBinding()]
param([string]$Destination = $env:RUNNER_TEMP)

$ErrorActionPreference = 'Stop'
if (-not $Destination) { throw 'Set RUNNER_TEMP or pass -Destination for the NSIS tool directory.' }
$version = '3.13'
$expectedSHA256 = 'ba63dffc4410ee89193e1cb5a41989991bd77c61068da17e3156d136b7b0b3d8'
$defaultUrl = "https://downloads.sourceforge.net/project/nsis/NSIS%203/$version/nsis-$version.zip"
# The default address lets SourceForge pick a mirror. When that mirror is slow,
# down or serves an HTML page, try a few long-standing mirrors by name. Every
# candidate serves the same file and is held to the same SHA-256 below.
$urls = @($defaultUrl) + @('netix', 'jaist', 'kumisystems' | ForEach-Object { $defaultUrl + '?use_mirror=' + $_ })
New-Item -ItemType Directory -Path $Destination -Force | Out-Null
$zip = Join-Path $Destination "nsis-$version.zip"

function Test-ZipMagic([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { return $false }
    $stream = [IO.File]::OpenRead($Path)
    try {
        $magic = New-Object byte[] 4
        $read = $stream.Read($magic, 0, 4)
    } finally { $stream.Dispose() }
    return ($read -eq 4 -and $magic[0] -eq 0x50 -and $magic[1] -eq 0x4B -and $magic[2] -eq 0x03 -and $magic[3] -eq 0x04)
}

# CI keeps the ZIP between runs. Reuse it only when it still matches the
# pinned checksum; anything else is downloaded again and verified below.
$cached = (Test-Path -LiteralPath $zip -PathType Leaf) -and (Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash -eq $expectedSHA256
if (-not $cached) {
    # SourceForge returns a browser download page (HTTP 200 HTML) for PowerShell's
    # default user agent. Use the command-line downloader it redirects to the ZIP,
    # and fall back to Invoke-WebRequest with a curl user-agent on Windows when
    # Schannel revocation checks fail offline. Each address gets few retries and
    # short timeouts so that trying every candidate stays bounded in time.
    $curl = if ($IsWindows) { 'curl.exe' } else { 'curl' }
    $partial = "$zip.partial-$([guid]::NewGuid().ToString('N'))"
    $downloaded = $false
    try {
        foreach ($url in $urls) {
            Remove-Item -LiteralPath $partial -Force -ErrorAction SilentlyContinue
            & $curl --fail --location --silent --show-error --retry 1 --retry-delay 3 --retry-all-errors --connect-timeout 10 --max-time 60 --output $partial --write-out "NSIS download: HTTP %{http_code}, %{content_type}, %{size_download} bytes\n" $url
            if ($LASTEXITCODE -ne 0) {
                Write-Warning "NSIS curl download from $url failed (exit $LASTEXITCODE)."
                if (-not $IsWindows) { continue }
                Remove-Item -LiteralPath $partial -Force -ErrorAction SilentlyContinue
                try {
                    Invoke-WebRequest -Uri $url -UserAgent 'curl/8.18.0' -OutFile $partial -TimeoutSec 60 -MaximumRedirection 5
                } catch {
                    Write-Warning "NSIS Invoke-WebRequest from $url failed: $($_.Exception.Message)"
                    continue
                }
            }
            if (Test-ZipMagic $partial) {
                Move-Item -LiteralPath $partial -Destination $zip -Force
                $downloaded = $true
                break
            }
            Write-Warning "NSIS download from $url is not a ZIP (SourceForge may have returned an HTML page); trying the next address."
        }
    } finally {
        if (Test-Path -LiteralPath $partial) { Remove-Item -LiteralPath $partial -Force -ErrorAction SilentlyContinue }
    }
    if (-not $downloaded) { throw "NSIS download failed from all $($urls.Count) addresses; nothing was installed." }
}

if (-not (Test-ZipMagic $zip)) {
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
