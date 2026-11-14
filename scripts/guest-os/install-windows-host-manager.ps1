#requires -Version 5.1
#requires -RunAsAdministrator
[CmdletBinding(SupportsShouldProcess)]
param(
    [Parameter(Mandatory)][guid]$VMId,
    [Parameter(Mandatory)][string]$HostSID,
    [Parameter(Mandatory)][string]$PayloadExecutable,
    [Parameter(Mandatory)][ValidatePattern('^[0-9a-fA-F]{64}$')][string]$PayloadSHA256
)
$ErrorActionPreference='Stop';$ProgressPreference='SilentlyContinue'
if($VMId -eq [guid]::Empty -or $HostSID -notmatch '^S-1-(5-21|12-1)-(\d+-){3}\d+$'){throw 'Select the exact VM and authorized host user.'}
# HostSID is the ordinary Cage user's identity carried across its UAC boundary.
# The administrator may be another account; its SID is never substituted here.
function Assert-NoLink([string]$Path){
    $item=Get-Item -LiteralPath $Path -Force
    while($item){if($item.Attributes -band [IO.FileAttributes]::ReparsePoint){throw 'Manager files cannot traverse links.'};if($item -is [IO.FileInfo]){$item=$item.Directory}else{$item=$item.Parent}}
}
function Protect-ManagerPath([string]$Path,[switch]$Shared){
    $item=Get-Item -LiteralPath $Path -Force
    if($item.Attributes -band [IO.FileAttributes]::ReparsePoint){throw 'Manager paths cannot be links.'}
    if($item -is [IO.DirectoryInfo]){$acl=[Security.AccessControl.DirectorySecurity]::new()}else{$acl=[Security.AccessControl.FileSecurity]::new()}
    $acl.SetOwner([Security.Principal.SecurityIdentifier]::new('S-1-5-32-544'));$acl.SetAccessRuleProtection($true,$false)
    $reader=if($Shared){'S-1-5-32-545'}else{$HostSID}
    foreach($id in @('S-1-5-18','S-1-5-32-544',$reader)){
        $rights=if($id -ceq $reader){'ReadAndExecute'}else{'FullControl'}
        if($item -is [IO.DirectoryInfo]){$rule=[Security.AccessControl.FileSystemAccessRule]::new([Security.Principal.SecurityIdentifier]::new($id),$rights,'ContainerInherit,ObjectInherit','None','Allow')}else{$rule=[Security.AccessControl.FileSystemAccessRule]::new([Security.Principal.SecurityIdentifier]::new($id),$rights,'Allow')}
        $acl.AddAccessRule($rule)
    }
    Set-Acl -LiteralPath $Path -AclObject $acl
}
$payload=Get-Item -LiteralPath $PayloadExecutable
if($payload -isnot [IO.FileInfo]){throw 'Select the built manager executable.'};Assert-NoLink $payload.FullName
if((Get-FileHash $payload.FullName -Algorithm SHA256).Hash.ToLowerInvariant() -cne $PayloadSHA256.ToLowerInvariant()){throw 'Manager payload checksum does not match.'}
$programRoot=Join-Path ([Environment]::GetFolderPath('ProgramFiles')) 'Cage Host Manager'
$program=Join-Path $programRoot $VMId.ToString('D');$exe=Join-Path $program 'Cage.HyperVManager.exe'
$cageDataRoot=Join-Path ([Environment]::GetFolderPath('CommonApplicationData')) 'Cage'
$dataRoot=Join-Path $cageDataRoot 'HyperVManager'
$data=Join-Path $dataRoot $VMId.ToString('D');$bindingPath=Join-Path $data 'binding.json'
$name='CageHV-'+$VMId.ToString('D');$binary='"'+$exe+'" service --binding "'+$bindingPath+'"'
if(!$PSCmdlet.ShouldProcess($VMId,'Install the protected local fixed-VM manager; keep VM, disks, accounts and host networking')){return}
foreach($path in @($programRoot,$program,$cageDataRoot,$dataRoot,$data)){
    if(!(Test-Path -LiteralPath $path)){New-Item -ItemType Directory -Path $path|Out-Null};Assert-NoLink $path
    Protect-ManagerPath $path -Shared:($path -in @($programRoot,$cageDataRoot,$dataRoot))
}
$prepare=Join-Path $program ('manager-prepare-'+[guid]::NewGuid().ToString('N')+'.exe')
try{
    # Hash and execute the same protected copy. The caller's original payload
    # may change while setup runs; it never becomes an execution authority.
    Copy-Item -LiteralPath $payload.FullName -Destination $prepare
    Protect-ManagerPath $prepare
    if((Get-FileHash $prepare -Algorithm SHA256).Hash.ToLowerInvariant() -cne $PayloadSHA256.ToLowerInvariant()){throw 'Protected manager preparation checksum does not match.'}
    $payload=Get-Item -LiteralPath $prepare
$service=Get-Service -Name $name -ErrorAction SilentlyContinue
if($service){
    $existing=Get-CimInstance Win32_Service -Filter ("Name='"+$name+"'")
    if($existing.PathName -cne $binary -or $existing.StartName -notin @('LocalSystem','NT AUTHORITY\SYSTEM')){throw 'An unrelated service already uses this name.'}
}
# Existing installations retain their protected authority and recovery journal.
# They can repair a stopped helper while its bound data volume is disconnected.
# Fresh registration verifies the actual VM using the protected payload copy.
$operation=if(Test-Path -LiteralPath $bindingPath){'binding'}else{'prepare'}
$raw=& $payload.FullName $operation --vm-id $VMId.ToString('D') --host-sid $HostSID
if($LASTEXITCODE -ne 0){throw 'The exact VM/disk binding could not be verified.'}
$binding=$raw|ConvertFrom-Json
if($binding.version -ne 1 -or [guid]$binding.vm_id -ne $VMId -or $binding.host_sid -cne $HostSID){throw 'The prepared manager binding does not match.'}
if(Test-Path -LiteralPath $bindingPath){
    Assert-NoLink $bindingPath;$old=Get-Content -LiteralPath $bindingPath -Raw -Encoding UTF8|ConvertFrom-Json
    foreach($key in @('version','vm_id','host_sid','disk_path','disk_volume','disk_file_id')){if([string]$old.$key -cne [string]$binding.$key){throw 'The existing protected VM/disk binding changed; preserved it.'}}
}
# Cage starts the registered manager on demand, so a status request during this
# update could restart the old image between the stop and the copy. Disabling
# the service holds it stopped; every path below restores automatic start.
# SCM also reports Stopped before the process has released its image.
function Stop-ManagerForReplacement{
    Set-Service -Name $name -StartupType Disabled
    $current=Get-CimInstance Win32_Service -Filter ("Name='"+$name+"'")
    if($current.PathName -cne $binary -or $current.StartName -notin @('LocalSystem','NT AUTHORITY\SYSTEM')){throw 'The fixed manager service identity changed before stopping; preserved the original files.'}
    $managerPID=[int]$current.ProcessId
    if((Get-Service -Name $name).Status -ne 'Stopped'){Stop-Service -Name $name}
    (Get-Service -Name $name).WaitForStatus('Stopped',[TimeSpan]::FromSeconds(30))
    $deadline=[DateTime]::UtcNow.AddSeconds(30)
    while($managerPID -gt 0){
        $process=Get-CimInstance Win32_Process -Filter ('ProcessId='+$managerPID)
        if(!$process -or ![string]::Equals([string]$process.ExecutablePath,$exe,[StringComparison]::OrdinalIgnoreCase)){break}
        if([DateTime]::UtcNow -gt $deadline){throw 'The stopped manager process did not exit in time; preserved the original files.'}
        Start-Sleep -Milliseconds 200
    }
}
# A scanner or the exiting process may still hold the image briefly.
function Copy-ManagerImage([string]$Source){
    for($attempt=1;;$attempt++){
        try{Copy-Item -LiteralPath $Source -Destination $exe -Force;return}
        catch{if($attempt -ge 20 -or $_.Exception -isnot [IO.IOException]){throw};Start-Sleep -Milliseconds 500}
    }
}
$rollback=$null;$cleanupRollback=$false;$replaced=$false;$wasRunning=$service -and $service.Status -eq 'Running'
try{
    if($service){
        Assert-NoLink $exe
        $rollback=Join-Path $program ('manager-rollback-'+[guid]::NewGuid().ToString('N')+'.exe')
        Copy-Item -LiteralPath $exe -Destination $rollback;Protect-ManagerPath $rollback
        Stop-ManagerForReplacement
    }
    $replaced=$true
    Copy-ManagerImage $payload.FullName;Protect-ManagerPath $exe
    if((Get-FileHash $exe -Algorithm SHA256).Hash.ToLowerInvariant() -cne $PayloadSHA256.ToLowerInvariant()){throw 'The copied manager checksum does not match.'}
    if(!(Test-Path -LiteralPath $bindingPath)){[IO.File]::WriteAllText($bindingPath,($binding|ConvertTo-Json -Depth 4),[Text.UTF8Encoding]::new($false))}
    Protect-ManagerPath $bindingPath
    if(!$service){New-Service -Name $name -BinaryPathName $binary -StartupType Automatic -DisplayName ('Cage fixed VM manager '+$VMId.ToString('D')) -Description ('Cage protected local VM manager '+$VMId.ToString('D'))|Out-Null}
    $sddl='O:SYG:SYD:(A;;GA;;;SY)(A;;GA;;;BA)(A;;CCLCRP;;;'+$HostSID+')'
    & ($env:windir+'\System32\sc.exe') sdset $name $sddl|Out-Null
    if($LASTEXITCODE -ne 0){throw 'The manager service permission could not be protected.'}
    & ($env:windir+'\System32\sc.exe') failure $name reset= 86400 actions= restart/5000/restart/15000/restart/60000|Out-Null
    if($LASTEXITCODE -ne 0){throw 'The manager recovery policy could not be configured.'}
    Set-Service -Name $name -StartupType Automatic
    Start-Service -Name $name;(Get-Service -Name $name).WaitForStatus('Running',[TimeSpan]::FromSeconds(30))
    $cleanupRollback=$true
}catch{
    if($rollback){
        try{
            # Nothing was replaced when the stop guard failed; the original
            # image stays in place and only automatic start is restored.
            if($replaced){
                Stop-ManagerForReplacement
                Copy-ManagerImage $rollback;Protect-ManagerPath $exe
            }
            $cleanupRollback=$true
        }finally{
            Set-Service -Name $name -StartupType Automatic -ErrorAction SilentlyContinue
            if($wasRunning){Start-Service -Name $name -ErrorAction SilentlyContinue}
        }
    }
    throw
}finally{if($rollback -and $cleanupRollback -and (Test-Path -LiteralPath $rollback)){Remove-Item -LiteralPath $rollback}}
[pscustomobject]@{Installed=$true;VMId=$VMId.ToString('D');HostSID=$HostSID;LocalPipeOnly=$true;VMDataPreserved=$true;HostGroupsUnchanged=$true;ServiceRunning=$true}
}finally{if(Test-Path -LiteralPath $prepare){Remove-Item -LiteralPath $prepare}}
