#requires -Version 5.1
[CmdletBinding()]
param(
    [Parameter(Mandatory)][ValidateSet('prepare', 'move', 'restore', 'cleanup', 'complete', 'abandon', 'uninstall', 'detach', 'attach')][string]$Operation,
    [Parameter(Mandatory)][string]$Target,
    [string]$Source,
    [string]$Receipt,
    [string]$Manifest,
    [string]$Backup,
    [string]$Diagnostics,
    [switch]$LegacyTarget,
    # Top-level folders that stay in the installation folder during an update.
    # A VM registered on a disk inside CageData keeps its path and handles.
    [string[]]$KeepInPlace = @()
)
$ErrorActionPreference = 'Stop'
$diagnosticStep = 'initializing'
# Which kind of entry blocked a move: never its name, path or message.
$blockedEntry = $null
trap {
    $failure = $_
    if ($Diagnostics) {
        try {
          if (!(Test-Path -LiteralPath $Diagnostics)) {
            $chain = @()
            $exception = $failure.Exception
            for ($index = 0; $exception -and $index -lt 4; $index++) {
                $chain += @{type = $exception.GetType().FullName; hresult = $exception.HResult}
                $exception = $exception.InnerException
            }
            $lines = @([regex]::Matches($failure.ScriptStackTrace, ': line (\d+)') | ForEach-Object { [int]$_.Groups[1].Value })
            @{operation = $Operation; phase = $diagnosticStep; pointerSize = [IntPtr]::Size; errors = $chain; lines = $lines; category = [string]$failure.CategoryInfo.Category; blocked = $blockedEntry} | ConvertTo-Json -Depth 4 |
                Set-Content -LiteralPath $Diagnostics -Encoding UTF8
          }
        } catch { }
    }
    throw $failure
}
$manifestName = 'Cage.install-files.json'
$identityName = 'Cage.install-id'

function Read-FileManifest([string]$Path) {
    $files = [Collections.Generic.Dictionary[string,string]]::new([StringComparer]::OrdinalIgnoreCase)
    if (!(Test-Path -LiteralPath $Path -PathType Leaf)) { return ,$files }
    if ((Get-Item -LiteralPath $Path -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) { return ,$files }
    try {
        $value = Get-Content -LiteralPath $Path -Raw -Encoding UTF8 | ConvertFrom-Json
        if ($value.version -ne 1) { throw 'Unknown installation manifest version.' }
        foreach ($entry in $value.files) {
            if (!$entry.path -or $entry.path -match '(^/|[\\:]|(^|/)\.\.?(/|$))' -or $entry.sha256 -notmatch '^[a-fA-F0-9]{64}$' -or $files.ContainsKey($entry.path)) {
                throw 'Invalid installation manifest entry.'
            }
            $files.Add($entry.path, $entry.sha256.ToLowerInvariant())
        }
    } catch {
        # An unreadable legacy manifest grants no permission to delete files.
        $files.Clear()
    }
    return ,$files
}

function Directory-Paths([string[]]$Paths) {
    $directories = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
    $null = $directories.Add('')
    foreach ($path in $Paths) {
        $parent = Split-Path $path -Parent
        while ($parent) { $null = $directories.Add($parent.Replace('\', '/')); $parent = Split-Path $parent -Parent }
    }
    return ,$directories
}

function Read-Tree([string]$Root, [string[]]$KnownPaths) {
    $entries = [Collections.Generic.List[object]]::new()
    $directories = [Collections.Generic.Queue[string]]::new()
    $neededDirectories = Directory-Paths $KnownPaths
    foreach ($path in $KnownPaths) { $null = $neededDirectories.Add($path) }
    $directories.Enqueue($Root)
    while ($directories.Count) {
        foreach ($entry in Get-ChildItem -LiteralPath $directories.Dequeue() -Force) {
            $entries.Add($entry)
            if ($entry.PSIsContainer -and !($entry.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
                if ($null -eq $KnownPaths -or $neededDirectories.Contains((Relative-Path $Root $entry.FullName))) { $directories.Enqueue($entry.FullName) }
            }
        }
    }
    return $entries.ToArray()
}

function Relative-Path([string]$Root, [string]$Path) {
    return $Path.Substring($Root.TrimEnd([IO.Path]::DirectorySeparatorChar).Length + 1).Replace('\', '/')
}

function Open-ReadLocked([string]$Path) {
    return [IO.File]::Open($Path, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read -bor [IO.FileShare]::Delete)
}

function Stream-Hash([IO.Stream]$Stream) {
    $algorithm = [Security.Cryptography.SHA256]::Create()
    try { return ([BitConverter]::ToString($algorithm.ComputeHash($Stream))).Replace('-', '').ToLowerInvariant() }
    finally { $algorithm.Dispose(); $Stream.Position = 0 }
}

# Windows PowerShell 5.1 compiles Add-Type with csc.exe, which cannot write its
# output below a temporary folder that the ANSI code page cannot encode and
# fails with "The directory name is invalid". The compile alone then uses the
# folder's 8.3 short form, or a private folder under the system temporary
# folder; the type is loaded in memory, so that folder is removed afterwards.
function Test-AnsiPath([string]$Path) {
    return [Text.Encoding]::Default.GetString([Text.Encoding]::Default.GetBytes($Path)) -eq $Path
}

function Compiler-TemporaryFolder {
    $temporary = [IO.Path]::GetTempPath()
    if ($PSVersionTable.PSEdition -ne 'Desktop' -or (Test-AnsiPath $temporary)) { return $null }
    if ($env:SystemRoot) {
        $private = Join-Path $env:SystemRoot ('Temp\cage-' + [guid]::NewGuid().ToString('N'))
        try {
            [IO.Directory]::CreateDirectory($private) | Out-Null
            return @{path = $private; created = $true}
        } catch { }
    }
    try {
        $short = (New-Object -ComObject Scripting.FileSystemObject).GetFolder($temporary).ShortPath
        if ($short -and (Test-AnsiPath $short) -and $short -notmatch '[?*]' -and [IO.Directory]::Exists($short)) {
            return @{path = $short; created = $false}
        }
    } catch { }
    return $null
}

function Initialize-VerifiedDeletion {
    if ('CageVerifiedFile' -as [type]) { return }
    # Read the C# source through .NET so csc.exe never receives a source file
    # path under a non-ANSI $PLUGINSDIR inside TEMP.
    $code = [IO.File]::ReadAllText((Join-Path $PSScriptRoot 'verified-file-deletion.cs'), [Text.Encoding]::UTF8)
    $folder = Compiler-TemporaryFolder
    $workDir = if ($env:SystemRoot -and [IO.Directory]::Exists($env:SystemRoot)) { $env:SystemRoot } elseif ($folder) { $folder.path } else { $null }
    if (!$folder -and !$workDir) { Add-Type -TypeDefinition $code; return }
    $saved = $env:TMP, $env:TEMP
    $savedDirectory = [Environment]::CurrentDirectory
    $pushed = $false
    try {
        if ($folder) { $env:TMP = $env:TEMP = $folder.path }
        if ($workDir) {
            Push-Location -LiteralPath $workDir
            $pushed = $true
            [Environment]::CurrentDirectory = $workDir
        }
        Add-Type -TypeDefinition $code
    } finally {
        if ($pushed) { Pop-Location }
        [Environment]::CurrentDirectory = $savedDirectory
        $env:TMP, $env:TEMP = $saved
        if ($folder -and $folder.created) {
            # A standard user cannot list the system temporary folder, so the
            # private folder is removed directly rather than with Remove-Item.
            try {
                foreach ($file in [IO.Directory]::GetFiles($folder.path)) { [IO.File]::Delete($file) }
                [IO.Directory]::Delete($folder.path)
            } catch { }
        }
    }
}

function Remove-MatchingFile([string]$Root, [string]$Relative, [string]$Hash) {
    $Path = Join-Path $Root $Relative
    if (!(Test-Path -LiteralPath $Path -PathType Leaf)) { return }
    if ((Get-Item -LiteralPath $Path -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) { return }
    $windows = $env:OS -eq 'Windows_NT'
    if ($windows) { Initialize-VerifiedDeletion; $stream = [CageVerifiedFile]::Open($Root, $Relative) }
    else { $stream = Open-ReadLocked $Path }
    try {
        if ((Stream-Hash $stream) -eq $Hash) {
            if ($windows) { [CageVerifiedFile]::Delete($stream) } else { [IO.File]::Delete($Path) }
        }
    }
    finally { $stream.Dispose() }
}

function Remove-EmptyDirectories([string]$Root, [string[]]$KnownDirectories) {
    foreach ($directory in @(Read-Tree $Root $KnownDirectories | Where-Object { $_.PSIsContainer -and !($_.Attributes -band [IO.FileAttributes]::ReparsePoint) } | Sort-Object { $_.FullName.Length } -Descending)) {
        if ((Relative-Path $Root $directory.FullName) -in $KnownDirectories -and @(Get-ChildItem -LiteralPath $directory.FullName -Force).Count -eq 0) {
            [IO.Directory]::Delete($directory.FullName)
        }
    }
}

function Preserved-Destination([string]$Relative, [bool]$Quarantine) {
    $path = Join-Path $Target $Relative
    $parent = Split-Path $path -Parent
    $conflict = $Quarantine -or (Test-Path -LiteralPath $path)
    while ($parent -ne $Target) {
        if (Test-Path -LiteralPath $parent -PathType Leaf) { $conflict = $true; break }
        $parent = Split-Path $parent -Parent
    }
    if ($conflict) { return $recovery + '/' + $Relative }
    return $Relative
}

function Program-Names([string]$Root) {
    $names = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
    foreach ($path in (Read-FileManifest (Join-Path $Root $manifestName)).Keys) { $null = $names.Add(($path -split '/')[0]) }
    foreach ($name in @($manifestName, 'Uninstall.exe')) { $null = $names.Add($name) }
    return ,$names
}

# Renaming the installation folder as a whole fails while any file below it is
# open without delete sharing, as a registered VM's disk or configuration can
# be, or while a process works in that folder. Moving its top-level entries
# one by one leaves kept folders and the folder itself in place. On failure
# every entry moved so far goes back.
function Move-TopLevel([string]$From, [string]$To, [string[]]$Keep, [switch]$RemoveEmptyTarget) {
    $programNames = Program-Names $From
    [IO.Directory]::CreateDirectory($To) | Out-Null
    $moved = [Collections.Generic.List[string]]::new()
    try {
        foreach ($entry in @(Get-ChildItem -LiteralPath $From -Force)) {
            if ($entry.Name -in $Keep) { continue }
            $script:blockedEntry = if ($programNames.Contains($entry.Name)) { 'program' }
                elseif ($entry.Name -eq 'Cage-preserved-files') { 'preserved' }
                elseif ($entry.Attributes -band [IO.FileAttributes]::ReparsePoint) { 'link' }
                else { 'personal' }
            $destination = Join-Path $To $entry.Name
            if (Get-Item -LiteralPath $destination -Force -ErrorAction SilentlyContinue) { throw 'The destination already has an entry of this name.' }
            if ($entry.Attributes -band [IO.FileAttributes]::Directory) { [IO.Directory]::Move($entry.FullName, $destination) }
            else { [IO.File]::Move($entry.FullName, $destination) }
            $moved.Add($entry.Name)
        }
        $script:blockedEntry = $null
    } catch {
        $failure = $_
        for ($index = $moved.Count - 1; $index -ge 0; $index--) {
            $current = Get-Item -LiteralPath (Join-Path $To $moved[$index]) -Force
            $original = Join-Path $From $moved[$index]
            if ($current.Attributes -band [IO.FileAttributes]::Directory) { [IO.Directory]::Move($current.FullName, $original) }
            else { [IO.File]::Move($current.FullName, $original) }
        }
        if ($RemoveEmptyTarget -and !@(Get-ChildItem -LiteralPath $To -Force).Count) { [IO.Directory]::Delete($To) }
        throw $failure
    }
}

function Save-Transfer($Transfer) {
    $temporary = $Receipt + '.' + [guid]::NewGuid().ToString('N') + '.tmp'
    try {
        $Transfer | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath $temporary -Encoding UTF8
        [IO.File]::Replace($temporary, $Receipt, [NullString]::Value)
    } finally { if (Test-Path -LiteralPath $temporary) { [IO.File]::Delete($temporary) } }
}

function Test-HasExistingContent([string]$Path) {
    if (!$Path) { return $false }
    $item = $null
    try { $item = Get-Item -LiteralPath $Path -Force -ErrorAction Stop }
    catch [Management.Automation.ItemNotFoundException], [IO.FileNotFoundException], [IO.DirectoryNotFoundException] {
        return $false
    } catch {
        return $true
    }
    if (!$item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint)) { return $true }
    try { return @(Get-ChildItem -LiteralPath $Path -Force -ErrorAction Stop).Count -gt 0 }
    catch { return $true }
}

# A running process maps the replaced version's program images, so cleanup can
# neither delete them nor move their folder. Bytes that still match the receipt
# hold no user data and need no recovery; only then may that folder remain.
function Test-OnlyProgramFiles([string]$Root, $Files) {
    $owned = @{}
    foreach ($entry in $Files) { $owned[$entry.path] = $entry.sha256 }
    $found = $false
    foreach ($item in Read-Tree $Root $null) {
        if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) { return $false }
        if ($item.PSIsContainer) { continue }
        $relative = Relative-Path $Root $item.FullName
        if (!$owned.ContainsKey($relative)) { return $false }
        try { $stream = Open-ReadLocked $item.FullName } catch { return $false }
        try { if ((Stream-Hash $stream) -ne $owned[$relative]) { return $false } } finally { $stream.Dispose() }
        $found = $true
    }
    return $found
}

function Complete-Transfer([string]$RetainedBackup) {
    $transfer = Get-Content -LiteralPath $Receipt -Raw -Encoding UTF8 | ConvertFrom-Json
    if ($transfer.kind -ne 'cage-installation' -or !(Test-Path -LiteralPath (Join-Path $Target $identityName))) { throw '安装目录尚未恢复，事务回执已保留。' }
    $retained = $RetainedBackup -and $transfer.backupPath -and
        [IO.Path]::GetFullPath($transfer.backupPath).TrimEnd([IO.Path]::DirectorySeparatorChar) -eq [IO.Path]::GetFullPath($RetainedBackup).TrimEnd([IO.Path]::DirectorySeparatorChar)
    if ($transfer.backupPath -and !$retained -and (Test-Path -LiteralPath $transfer.backupPath)) { throw ('旧目录仍需恢复，事务回执已保留：' + $transfer.backupPath) }
    foreach ($entry in $transfer.moves) {
        if ($entry.state -eq 'absent') { continue }
        $relative = if ($entry.state -eq 'moved') { $entry.destination } else { $entry.path }
        if (!(Get-Item -LiteralPath (Join-Path $Target $relative) -Force -ErrorAction SilentlyContinue)) { throw ('个人文件尚未恢复，事务回执已保留：' + $relative) }
    }
    $receiptStream = Open-ReadLocked $Receipt
    try { $hash = Stream-Hash $receiptStream } finally { $receiptStream.Dispose() }
    Remove-MatchingFile (Split-Path $Receipt -Parent) (Split-Path $Receipt -Leaf) $hash
    if (Test-Path -LiteralPath $Receipt) { throw '安装回执在完成期间发生变化，已保留待核对。' }
}

$Target = [IO.Path]::GetFullPath($Target).TrimEnd([IO.Path]::DirectorySeparatorChar)
foreach ($rootPath in @($Target, $Source)) {
    if (!$rootPath) { continue }
    $rootEntry = Get-Item -LiteralPath $rootPath -Force -ErrorAction SilentlyContinue
    if ($rootEntry -and $rootEntry.Attributes -band [IO.FileAttributes]::ReparsePoint) {
        throw ('安装目录本身是文件系统链接，原文件已保留。请选择实际程序目录：' + $rootPath)
    }
}
if ($Operation -eq 'prepare') {
    $diagnosticStep = 'reading_new_manifest'
    if (Test-Path -LiteralPath $Receipt) { throw ('仍有未完成的安装事务，请保留并检查回执：' + $Receipt) }
    # Completing the receipt deletes it through the verified-deletion type.
    # Compiling it before the receipt exists stops an installation that could
    # not complete before any file is published.
    if ($env:OS -eq 'Windows_NT') {
        $diagnosticStep = 'loading_verified_deletion'
        Initialize-VerifiedDeletion
        $diagnosticStep = 'reading_new_manifest'
    }
    $current = Read-FileManifest (Join-Path $Target $manifestName)
    $legacySnapshot = !$current.Count -and $LegacyTarget
    if ($legacySnapshot) {
        $identity = Open-ReadLocked (Join-Path $Target $identityName)
        try { $current[$identityName] = Stream-Hash $identity } finally { $identity.Dispose() }
    }
    if (!$current.Count) { throw '新程序缺少有效文件清单，安装已停止。' }
    $diagnosticStep = 'reading_new_uninstaller'
    $uninstaller = Open-ReadLocked (Join-Path $Target 'Uninstall.exe')
    try { $current['Uninstall.exe'] = Stream-Hash $uninstaller } finally { $uninstaller.Dispose() }
    $transfers = [Collections.Generic.List[object]]::new()
    $moves = [Collections.Generic.List[object]]::new()
    $directories = @()
    $recovery = 'Cage-preserved-files/' + [guid]::NewGuid().ToString('N')
    $programDirectories = Directory-Paths @($current.Keys)
    if ($Source -and (Test-Path -LiteralPath $Source -PathType Container)) {
        $diagnosticStep = 'scanning_previous_installation'
        $Source = [IO.Path]::GetFullPath($Source).TrimEnd([IO.Path]::DirectorySeparatorChar)
        $previous = Read-FileManifest (Join-Path $Source $manifestName)
        $previousDirectories = Directory-Paths @($previous.Keys)
        $pending = [Collections.Generic.Queue[string]]::new()
        $pending.Enqueue($Source)
        while ($pending.Count) {
          foreach ($entry in Get-ChildItem -LiteralPath $pending.Dequeue() -Force) {
            $relative = Relative-Path $Source $entry.FullName
            if ($relative -in $KeepInPlace) { continue }
            $isLink = [bool]($entry.Attributes -band [IO.FileAttributes]::ReparsePoint)
            $programContent = $previous.ContainsKey($relative) -or (
                $programDirectories.Contains((Split-Path $relative -Parent).Replace('\', '/')) -and
                ($entry.Extension -in @('.dll','.exe','.ocx','.sys','.pyd','.so','.dylib') -or $entry.Name -eq 'node_modules'))
            $hasOwnedChildren = $previousDirectories.Contains($relative)
            # A legacy rollback snapshot also contains old personal files.
            # The failed candidate's manifest identifies its program paths, so
            # current personal content can return to its original location.
            if ($legacySnapshot -and $previous.Count -and !$programContent -and !$hasOwnedChildren -and
                $relative -notin @($manifestName, 'Cage-preserved-files')) {
                $oldCopy = Get-Item -LiteralPath (Join-Path $Target $relative) -Force -ErrorAction SilentlyContinue
                if ($oldCopy) {
                    $oldDestination = Join-Path $Target ($recovery + '/previous/' + $relative)
                    [IO.Directory]::CreateDirectory((Split-Path $oldDestination -Parent)) | Out-Null
                    if ($oldCopy.Attributes -band [IO.FileAttributes]::Directory) { [IO.Directory]::Move($oldCopy.FullName, $oldDestination) }
                    else { [IO.File]::Move($oldCopy.FullName, $oldDestination) }
                }
            }
            if ($isLink -or ($entry.PSIsContainer -and !$hasOwnedChildren -and !(Test-Path -LiteralPath (Join-Path $Target $relative) -PathType Container))) {
                $moves.Add(@{path = $relative; destination = (Preserved-Destination $relative $programContent); directory = ($entry.PSIsContainer -or [bool]($entry.Attributes -band [IO.FileAttributes]::Directory)); state = 'prepared'})
                continue
            }
            if ($entry.PSIsContainer) {
                $directories += $relative
                $pending.Enqueue($entry.FullName)
                continue
            }
            if (!$previous.ContainsKey($relative) -and !(Test-Path -LiteralPath (Join-Path $Target $relative)) -and $relative -ne $manifestName) {
                $moves.Add(@{path = $relative; destination = (Preserved-Destination $relative $programContent); directory = $false; state = 'prepared'})
                continue
            }
            $sourceStream = Open-ReadLocked $entry.FullName
            try {
                $hash = Stream-Hash $sourceStream
                if (($previous.ContainsKey($relative) -and $previous[$relative] -eq $hash) -or ($relative -eq $manifestName -and $previous.Count)) {
                    $transfers.Add(@{ path = $relative; sha256 = $hash; destination = $null })
                    continue
                }
                $path = Join-Path $Target $relative
                if (Test-Path -LiteralPath $path -PathType Leaf) {
                    $candidate = Open-ReadLocked $path
                    try { $same = (Stream-Hash $candidate) -eq $hash } finally { $candidate.Dispose() }
                    if ($same) {
                        $transfers.Add(@{ path = $relative; sha256 = $hash; destination = $relative })
                        continue
                    }
                }
                $moves.Add(@{path = $relative; destination = (Preserved-Destination $relative $programContent); directory = $false; state = 'prepared'})
            } finally { $sourceStream.Dispose() }
          }
        }
    }
    $diagnosticStep = 'writing_new_manifest'
    $files = @($current.Keys | Sort-Object | ForEach-Object { @{ path = $_; sha256 = $current[$_] } })
    @{ version = 1; files = $files } | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath (Join-Path $Target $manifestName) -Encoding UTF8
    $diagnosticStep = 'writing_transaction_receipt'
    $record = @{kind = 'cage-installation'; version = 1; installPath = $Source; stagePath = $Target; backupPath = $Backup; files = @($transfers); directories = $directories; moves = @($moves); keepInPlace = @($KeepInPlace)}
    $encoding = [Text.UTF8Encoding]::new($true)
    [byte[]]$bytes = $encoding.GetPreamble() + $encoding.GetBytes(($record | ConvertTo-Json -Depth 6))
    $temporary = $Receipt + '.' + [guid]::NewGuid().ToString('N') + '.tmp'
    try {
        $output = [IO.File]::Open($temporary, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
        try { $output.Write($bytes, 0, $bytes.Length); $output.Flush($true) } finally { $output.Dispose() }
        [IO.File]::Move($temporary, $Receipt)
    } finally { if (Test-Path -LiteralPath $temporary) { [IO.File]::Delete($temporary) } }
} elseif ($Operation -in @('move','restore')) {
    $diagnosticStep = 'moving_preserved_files'
    $transfer = Get-Content -LiteralPath $Receipt -Raw -Encoding UTF8 | ConvertFrom-Json
    $moves = @($transfer.moves)
    if ($Operation -eq 'restore') { [Array]::Reverse($moves) }
    foreach ($entry in $moves) {
        if ($Operation -eq 'restore' -and $entry.state -notin @('moving','moved')) { continue }
        $from = if ($Operation -eq 'move') { $entry.path } else { $entry.destination }
        $to = if ($Operation -eq 'move') { $entry.destination } else { $entry.path }
        $from = Join-Path $Source $from
        $to = Join-Path $Target $to
        if (!(Get-Item -LiteralPath $from -Force -ErrorAction SilentlyContinue)) {
            if ($Operation -eq 'restore' -and !(Get-Item -LiteralPath $to -Force -ErrorAction SilentlyContinue)) { throw ('待恢复的个人文件无法定位，回执已保留：' + $entry.path) }
            if ($Operation -eq 'move' -and $entry.state -eq 'prepared') { $entry.state = 'absent'; Save-Transfer $transfer }
            continue
        }
        [IO.Directory]::CreateDirectory((Split-Path $to -Parent)) | Out-Null
        if ($Operation -eq 'move') { $entry.state = 'moving'; Save-Transfer $transfer }
        $moved = $false
        try {
            if ($entry.directory) { [IO.Directory]::Move($from, $to) } else { [IO.File]::Move($from, $to) }
            $moved = $true
            $entry.state = if ($Operation -eq 'move') { 'moved' } else { 'restored' }
            Save-Transfer $transfer
        } catch {
            if ($Operation -eq 'move' -and !$moved) { $entry.state = 'failed'; Save-Transfer $transfer }
            throw
        }
    }
    if ($Operation -eq 'move' -and @($moves | Where-Object { $_.destination.StartsWith('Cage-preserved-files/') }).Count) {
        Write-Output ('同名修改或旧程序文件已保留在 ' + (Join-Path $Target 'Cage-preserved-files'))
    }
} elseif ($Operation -eq 'cleanup') {
    $diagnosticStep = 'verifying_previous_file_ownership'
    $transfer = Get-Content -LiteralPath $Receipt -Raw -Encoding UTF8 | ConvertFrom-Json
    $actual = @(Read-Tree $Source @($transfer.files.path) | Where-Object { !($_.Attributes -band [IO.FileAttributes]::ReparsePoint) } | ForEach-Object { Relative-Path $Source $_.FullName })
    foreach ($entry in $transfer.files) {
        if ($entry.path -notin $actual) { continue }
        try {
            if ($entry.destination) {
                $copy = Open-ReadLocked (Join-Path $Target $entry.destination)
                try { if ((Stream-Hash $copy) -ne $entry.sha256) { continue } } finally { $copy.Dispose() }
            }
            Remove-MatchingFile $Source $entry.path $entry.sha256
        } catch { Write-Output '安装期间变化或被占用的文件已保留在旧目录。' }
    }
    Remove-EmptyDirectories $Source $transfer.directories
    $retainedBackup = $null
    if (@(Get-ChildItem -LiteralPath $Source -Force).Count) {
        $recovery = Join-Path $Target ('Cage-preserved-files/late-' + [guid]::NewGuid().ToString('N'))
        $preserved = Split-Path $recovery -Parent
        $createdPreserved = !(Test-Path -LiteralPath $preserved)
        [IO.Directory]::CreateDirectory($preserved) | Out-Null
        try {
            [IO.Directory]::Move($Source, $recovery)
            Write-Output ('安装期间变化的文件已保留在 ' + $recovery)
        } catch {
            if (!(Test-OnlyProgramFiles $Source $transfer.files)) { throw }
            if ($createdPreserved) { [IO.Directory]::Delete($preserved) }
            $retainedBackup = $Source
            Write-Output ('被替换的程序文件仍被占用，已留在 ' + $Source + '，退出相关程序后可删除。')
        }
    } else { [IO.Directory]::Delete($Source) }
    Complete-Transfer $retainedBackup
} elseif ($Operation -eq 'detach') {
    # Source is the installation folder, Target the new backup folder.
    $diagnosticStep = 'detaching_previous_application'
    $transfer = Get-Content -LiteralPath $Receipt -Raw -Encoding UTF8 | ConvertFrom-Json
    if ($transfer.kind -ne 'cage-installation' -or !$Source) { throw '安装事务回执无效，原安装目录已保留。' }
    if (Test-Path -LiteralPath $Target) { throw '备份目录已存在，原安装目录已保留。' }
    $Source = [IO.Path]::GetFullPath($Source).TrimEnd([IO.Path]::DirectorySeparatorChar)
    Move-TopLevel $Source $Target @($transfer.keepInPlace) -RemoveEmptyTarget
} elseif ($Operation -eq 'attach') {
    # Source is a complete program folder: the staged update, or the previous
    # application during a rollback. Target is the installation folder.
    $diagnosticStep = 'attaching_application'
    if (!$Source) { throw '缺少待发布的程序目录。' }
    $Source = [IO.Path]::GetFullPath($Source).TrimEnd([IO.Path]::DirectorySeparatorChar)
    Move-TopLevel $Source $Target @()
    if (@(Get-ChildItem -LiteralPath $Source -Force).Count) { throw '程序目录仍有未发布的文件，已保留待核对。' }
    [IO.Directory]::Delete($Source)
} elseif ($Operation -eq 'complete') {
    $diagnosticStep = 'completing_transaction_receipt'
    Complete-Transfer
} elseif ($Operation -eq 'abandon') {
    # A first installation that could not publish its staged folder has no
    # previous installation, backup or moved entry to recover. Only then is
    # its receipt removed, so that the installation can be retried.
    $diagnosticStep = 'abandoning_first_installation'
    $transfer = Get-Content -LiteralPath $Receipt -Raw -Encoding UTF8 | ConvertFrom-Json
    if ($transfer.kind -ne 'cage-installation' -or $transfer.backupPath -or @($transfer.moves).Count -or @($transfer.files).Count -or
        @($transfer.directories).Count -or (Test-HasExistingContent $transfer.installPath) -or (Test-HasExistingContent $Target)) {
        throw '安装事务涉及已有文件，回执已保留待核对。'
    }
    $receiptStream = Open-ReadLocked $Receipt
    try { $hash = Stream-Hash $receiptStream } finally { $receiptStream.Dispose() }
    Remove-MatchingFile (Split-Path $Receipt -Parent) (Split-Path $Receipt -Leaf) $hash
    if (Test-Path -LiteralPath $Receipt) { throw '安装回执在完成期间发生变化，已保留待核对。' }
} else {
    # The payload list comes from this uninstaller's embedded build manifest.
    $diagnosticStep = 'reading_uninstall_manifest'
    $owned = Read-FileManifest $Manifest
    if (!$owned.Count) { throw '卸载文件清单无效，原文件已保留。' }
    $installed = Read-FileManifest (Join-Path $Target $manifestName)
    if ($installed.ContainsKey('Uninstall.exe')) { $owned['Uninstall.exe'] = $installed['Uninstall.exe'] }
    $actual = @(Read-Tree $Target @($owned.Keys) | Where-Object { !($_.Attributes -band [IO.FileAttributes]::ReparsePoint) } | ForEach-Object { Relative-Path $Target $_.FullName })
    $diagnosticStep = 'deleting_verified_program_files'
    foreach ($relative in $owned.Keys) {
        if ($relative -ne $identityName -and $relative -in $actual) { Remove-MatchingFile $Target $relative $owned[$relative] }
    }
    if ($installed.Count) {
        $manifestStream = Open-ReadLocked (Join-Path $Target $manifestName)
        try { $manifestHash = Stream-Hash $manifestStream } finally { $manifestStream.Dispose() }
        Remove-MatchingFile $Target $manifestName $manifestHash
    }
    $directories = @()
    foreach ($relative in $owned.Keys) {
        $directory = Split-Path $relative -Parent
        while ($directory) { $directories += $directory.Replace('\', '/'); $directory = Split-Path $directory -Parent }
    }
    $diagnosticStep = 'removing_program_directories'
    Remove-EmptyDirectories $Target $directories
    # A retained identity lets a later installation reuse this data-bearing folder.
    $remaining = @(Get-ChildItem -LiteralPath $Target -Force | Where-Object Name -ne $identityName)
    if (!$remaining.Count) {
        if ($owned.ContainsKey($identityName)) { Remove-MatchingFile $Target $identityName $owned[$identityName] }
        if (@(Get-ChildItem -LiteralPath $Target -Force).Count -eq 0) { [IO.Directory]::Delete($Target) }
    } else { Write-Output ('用户新增或修改的文件已保留在 ' + $Target) }
}
