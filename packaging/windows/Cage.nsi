Unicode true
RequestExecutionLevel user
; Per-file decompression avoids a full payload copy in the system TEMP drive.
SetCompressor lzma
SetCompressorDictSize 32
ManifestDPIAware true
ManifestSupportedOS all

!include MUI2.nsh
!define MUI_ICON "Cage.ico"
!define MUI_UNICON "Cage.ico"
!include LogicLib.nsh
!include FileFunc.nsh
!include x64.nsh
!include WinVer.nsh
!include nsDialogs.nsh

!ifndef APP_DIR
  !error "APP_DIR is required"
!endif
!ifndef VERSION
  !error "VERSION is required"
!endif
!ifndef VERSION_NUMERIC
  !error "VERSION_NUMERIC is required"
!endif
!ifndef OUTPUT_FILE
  !error "OUTPUT_FILE is required"
!endif

!define APP_ID "io.github.lee-xeo.claude-cage"
!define UNINSTALL_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\ClaudeCage"
!define WEBVIEW2_APP_ID "{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}"
Name "Cage ${VERSION}"
OutFile "${OUTPUT_FILE}"
InstallDir "$LOCALAPPDATA\Programs\Cage"
InstallDirRegKey HKCU "${UNINSTALL_KEY}" "InstallLocation"
VIProductVersion "${VERSION_NUMERIC}"
VIAddVersionKey /LANG=1033 "ProductName" "Cage"
VIAddVersionKey /LANG=1033 "ProductVersion" "${VERSION}"
VIAddVersionKey /LANG=1033 "FileVersion" "${VERSION}"
VIAddVersionKey /LANG=1033 "FileDescription" "Cage Windows Installer"
VIAddVersionKey /LANG=1033 "LegalCopyright" "Cage contributors"

!ifdef SIGN_SCRIPT
  !uninstfinalize '"${SIGN_PWSH}" -NoProfile -File "${SIGN_SCRIPT}" -Path "%1"' = 0
!endif

!define MUI_ABORTWARNING
!define MUI_LANGDLL_ALLLANGUAGES
!define MUI_LANGDLL_WINDOWTITLE "Cage"
!define MUI_LANGDLL_INFO "$(CageInstallerLanguage)"
!define MUI_FINISHPAGE_RUN "$INSTDIR\Cage.exe"
!define MUI_FINISHPAGE_RUN_NOTCHECKED
!define MUI_FINISHPAGE_TEXT "$(CageFinishHelp)"
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_LICENSE "${APP_DIR}/LICENSE.txt"
!define MUI_DIRECTORYPAGE_TEXT_TOP "$(CageDirectoryHelp)"
!insertmacro MUI_PAGE_DIRECTORY
Page custom ShortcutOptionsPage ShortcutOptionsLeave
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!include "CageInstallerOptions.nsh"
Var StageDir
Var BackupDir
Var ExistingInstall
Var DesktopShortcutControl
Var StartMenuShortcutsControl
Var InstallationMutex
Var TransactionRecord
Var InstallPhase
Var InstallOutcome
Var HelperExit
Var DiagnosticArguments
Var InstallerSystemLanguage

!include "CageInstallerLanguages.nsh"
!include "CageInstallerLanguageSelection.nsh"

Function WriteInstallDiagnostic
  ${If} $DiagnosticFile != ""
    Push $2
    Push $3
    StrCpy $3 0
    IfErrors 0 +2
      StrCpy $3 1
    ClearErrors
    FileOpen $2 "$DiagnosticFile" w
    ${IfNot} ${Errors}
      FileWrite $2 '{"phase":"$InstallPhase","status":"$InstallOutcome","helperExit":"$HelperExit"}$\r$\n'
      FileClose $2
    ${EndIf}
    ${If} $3 == 1
      SetErrors
    ${Else}
      ClearErrors
    ${EndIf}
    Pop $3
    Pop $2
  ${EndIf}
FunctionEnd

!macro InstallPhase NAME
  StrCpy $InstallPhase "${NAME}"
  Call WriteInstallDiagnostic
!macroend

Function un.WriteInstallDiagnostic
  ${If} $DiagnosticFile != ""
    Push $2
    Push $3
    StrCpy $3 0
    IfErrors 0 +2
      StrCpy $3 1
    ClearErrors
    FileOpen $2 "$DiagnosticFile" w
    ${IfNot} ${Errors}
      FileWrite $2 '{"phase":"$InstallPhase","status":"$InstallOutcome","helperExit":"$HelperExit"}$\r$\n'
      FileClose $2
    ${EndIf}
    ${If} $3 == 1
      SetErrors
    ${Else}
      ClearErrors
    ${EndIf}
    Pop $3
    Pop $2
  ${EndIf}
FunctionEnd

!macro UninstallPhase NAME
  StrCpy $InstallPhase "${NAME}"
  Call un.WriteInstallDiagnostic
!macroend

Function un.onUninstFailed
  StrCpy $InstallOutcome "failed"
  Call un.WriteInstallDiagnostic
FunctionEnd

Function un.onUninstSuccess
  StrCpy $InstallOutcome "success"
  !insertmacro UninstallPhase "completed"
FunctionEnd

Function .onInstFailed
  StrCpy $InstallOutcome "failed"
  Call WriteInstallDiagnostic
FunctionEnd

Function .onInstSuccess
  StrCpy $InstallOutcome "success"
  Call WriteInstallDiagnostic
FunctionEnd

!macro AcquireInstallationMutex
  ; NSIS and UpdateInstaller both reject an existing named object and keep
  ; their handle until the transaction ends. No caller waits for ownership.
  System::Call 'kernel32::CreateMutexW(p 0, i 0, w "Global\Cage.Installation") p.r0 ?e'
  Pop $1
  ${If} $0 == 0
    SetErrorLevel 2
    Abort "$(CageSetupDetail0)"
  ${EndIf}
  StrCpy $InstallationMutex $0
  ${If} $1 == 183
    SetErrorLevel 2
    Abort "$(CageSetupDetail1)"
  ${EndIf}
!macroend

Function .onGUIEnd
  ${If} $InstallationMutex != ""
    System::Call 'kernel32::CloseHandle(p $InstallationMutex)'
  ${EndIf}
FunctionEnd

Function un.onGUIEnd
  ${If} $InstallationMutex != ""
    System::Call 'kernel32::CloseHandle(p $InstallationMutex)'
  ${EndIf}
FunctionEnd

Function LoadShortcutOptions
  ; Fresh installs enable both entries. Older installs inherit actual shortcuts.
  StrCpy $DesktopShortcut 1
  StrCpy $StartMenuShortcuts 1
  ReadRegStr $0 HKCU "${UNINSTALL_KEY}" "InstallLocation"
  ${If} $0 != ""
    StrCpy $DesktopShortcut 0
    StrCpy $StartMenuShortcuts 0
    ${If} ${FileExists} "$DESKTOP\Cage.lnk"
      StrCpy $DesktopShortcut 1
    ${EndIf}
    ${If} ${FileExists} "$SMPROGRAMS\Cage\Cage.lnk"
      StrCpy $StartMenuShortcuts 1
    ${EndIf}
  ${EndIf}
  ClearErrors
  ReadRegDWORD $0 HKCU "${UNINSTALL_KEY}" "DesktopShortcut"
  ${IfNot} ${Errors}
    StrCpy $DesktopShortcut $0
  ${EndIf}
  ClearErrors
  ReadRegDWORD $0 HKCU "${UNINSTALL_KEY}" "StartMenuShortcuts"
  ${IfNot} ${Errors}
    StrCpy $StartMenuShortcuts $0
  ${EndIf}
FunctionEnd

Function ShortcutOptionsPage
  !insertmacro MUI_HEADER_TEXT "$(CageShortcutsTitle)" "$(CageShortcutsHelp)"
  nsDialogs::Create 1018
  Pop $0
  ${If} $0 == error
    Abort
  ${EndIf}
  nsDialogs::SetRTL $(^RTL)
  ${NSD_CreateCheckbox} 0 8u 100% 16u "$(CageDesktopShortcut)"
  Pop $DesktopShortcutControl
  ${NSD_SetState} $DesktopShortcutControl $DesktopShortcut
  ${NSD_CreateCheckbox} 0 36u 100% 16u "$(CageStartMenuShortcuts)"
  Pop $StartMenuShortcutsControl
  ${NSD_SetState} $StartMenuShortcutsControl $StartMenuShortcuts
  ${NSD_OnBack} ShortcutOptionsLeave
  nsDialogs::Show
FunctionEnd

Function ShortcutOptionsLeave
  ${NSD_GetState} $DesktopShortcutControl $DesktopShortcut
  ${NSD_GetState} $StartMenuShortcutsControl $StartMenuShortcuts
FunctionEnd

Function SetDefaultInstallDirectory
  ${If} $CageInstallDirectory != ""
    StrCpy $INSTDIR $CageInstallDirectory
    Return
  ${EndIf}
  ; Keep upgrades in the registered location, including installations on C:.
  ReadRegStr $0 HKCU "${UNINSTALL_KEY}" "InstallLocation"
  ${If} $0 != ""
    StrCpy $INSTDIR $0
    Return
  ${EndIf}
  System::Call 'kernel32::GetDriveTypeW(w "D:\") i .r0'
  ${If} $0 == 3
    StrCpy $INSTDIR "D:\Cage"
  ${EndIf}
FunctionEnd

!macro CheckStopped FILE
  ${If} ${FileExists} "$INSTDIR\${FILE}"
    ; Exclusive write access also detects images whose VM/controller has no window.
    ; Process exit can precede release of its executable's sharing lock.
    ; Retry sharing/lock violations briefly; never replace an image still in use.
    Push $1
    Push $2
    StrCpy $1 0
    ${Do}
      System::Call 'kernel32::CreateFileW(w "$INSTDIR\${FILE}", i 0x40000000, i 0, p 0, i 3, i 0, p 0) p .r0 ?e'
      Pop $2
      ${If} $0 != -1
        ${ExitDo}
      ${EndIf}
      ${If} $2 != 32
      ${AndIf} $2 != 33
        ${ExitDo}
      ${EndIf}
      IntOp $1 $1 + 1
      ${If} $1 >= 30
        ${ExitDo}
      ${EndIf}
      Sleep 100
    ${Loop}
    Pop $2
    Pop $1
    ${If} $0 == -1
      StrCpy $HelperExit "application_file_in_use"
      SetErrorLevel 2
      Abort "$(CageSetupStop)"
    ${EndIf}
    System::Call 'kernel32::CloseHandle(p r0)'
  ${EndIf}
!macroend

Function .onInit
  !insertmacro CageSelectInstallerLanguage
  StrCpy $InstallerSystemLanguage $LANGUAGE
  StrCpy $InstallOutcome "running"
  StrCpy $HelperExit ""
  SetShellVarContext current
  SetRegView 64
  Call LoadShortcutOptions
  Call ParseCageInstallOptions
  ${If} $CageOptionError != ""
    StrCpy $InstallPhase "parsing_arguments"
    StrCpy $InstallOutcome "failed"
    StrCpy $HelperExit "invalid_installer_arguments"
    Call WriteInstallDiagnostic
    MessageBox MB_ICONSTOP "$CageOptionError" /SD IDOK
    SetErrorLevel 2
    Quit
  ${EndIf}
  StrCpy $DiagnosticArguments ""
  ${If} $DiagnosticFile != ""
    StrCpy $DiagnosticArguments '-Diagnostics "$DiagnosticFile.helper.json"'
  ${EndIf}
  !insertmacro InstallPhase "initializing"
  ${IfNot} ${IsNativeAMD64}
    MessageBox MB_ICONSTOP "$(CageSetupArch)"
    SetErrorLevel 1633
    Quit
  ${EndIf}
  ReadRegStr $0 HKLM "SOFTWARE\Microsoft\Windows NT\CurrentVersion" "CurrentBuildNumber"
  ${If} $0 < 19045
    MessageBox MB_ICONSTOP "$(CageSetupWindows)"
    SetErrorLevel 1633
    Quit
  ${EndIf}
  Call SetDefaultInstallDirectory
  !insertmacro MUI_LANGDLL_DISPLAY
FunctionEnd

Function CheckDestination
  ${GetRoot} "$INSTDIR" $0
  ${If} $0 == ""
  ${OrIf} $INSTDIR == $0
  ${OrIf} $INSTDIR == "$0\"
    SetErrorLevel 2
    Abort "$(CageSetupDedicated)"
  ${EndIf}
  StrCpy $ExistingInstall "0"
  IfFileExists "$INSTDIR\Cage.install-id" existing
  ; IfFileExists with a wildcard also matches an empty directory.
  ${DirState} "$INSTDIR" $0
  StrCmp $0 1 foreign done
  existing:
    FileOpen $0 "$INSTDIR\Cage.install-id" r
    FileRead $0 $1
    FileClose $0
    ${If} $1 != "${APP_ID}"
      Goto foreign
    ${EndIf}
    StrCpy $ExistingInstall "1"
    ; Never stop processes by their name or force-close user workloads.
    !insertmacro CheckStopped "Cage.exe"
    !insertmacro CheckStopped "resources\cage.exe"
    !insertmacro CheckStopped "resources\mihomo.exe"
    !insertmacro CheckStopped "resources\runtime\qemu\qemu-system-x86_64.exe"
    Goto done
  foreign:
    SetErrorLevel 2
    Abort "$(CageSetupOtherFiles)"
  done:
    ClearErrors
FunctionEnd

Function RestorePreservedFiles
  nsExec::ExecToStack '"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -ExecutionPolicy Bypass -File "$PLUGINSDIR\installation-files.ps1" -Operation restore -Target "$BackupDir" -Source "$StageDir" -Receipt "$TransactionRecord" $DiagnosticArguments'
  Pop $0
  Pop $1
  ${If} $0 != 0
    DetailPrint "$1"
    SetErrorLevel 2
    Abort "$(CageSetupDetail2)"
  ${EndIf}
  ; The installation folder was kept, so the previous entries move back into it.
  nsExec::ExecToStack '"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -ExecutionPolicy Bypass -File "$PLUGINSDIR\installation-files.ps1" -Operation attach -Target "$INSTDIR" -Source "$BackupDir" $DiagnosticArguments'
  Pop $0
  Pop $1
  ${If} $0 != 0
    DetailPrint "$1"
    SetErrorLevel 2
    Abort "$(CageSetupDetail3)"
  ${EndIf}
  nsExec::ExecToStack '"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -ExecutionPolicy Bypass -File "$PLUGINSDIR\installation-files.ps1" -Operation complete -Target "$INSTDIR" -Receipt "$TransactionRecord" $DiagnosticArguments'
  Pop $0
  Pop $1
  ${If} $0 != 0
    SetErrorLevel 2
    Abort "$(CageSetupDetail4)"
  ${EndIf}
FunctionEnd

Section "Cage" Main
  !insertmacro InstallPhase "acquiring_installation_lock"
  !insertmacro AcquireInstallationMutex
  StrCpy $TransactionRecord "$INSTDIR.cage-installation.json"
  ${If} ${FileExists} "$TransactionRecord"
    SetErrorLevel 2
    Abort "$(CageSetupDetail5)"
  ${EndIf}
  !insertmacro InstallPhase "checking_destination"
  Call CheckDestination
  ${GetParent} "$INSTDIR" $0
  CreateDirectory "$0"
  GetTempFileName $StageDir "$0"
  Delete "$StageDir"
  CreateDirectory "$StageDir"
  !insertmacro InstallPhase "unpacking_application"
  SetOutPath "$StageDir"
  ClearErrors
!ifdef NSIS_WIN32_MAKENSIS
  File /r "${APP_DIR}\*"
!else
  File /r "${APP_DIR}/*"
!endif
  WriteUninstaller "$StageDir\Uninstall.exe"
  ${If} ${Errors}
    SetOutPath "$TEMP"
    RMDir /r "$StageDir"
    SetErrorLevel 2
    Abort "$(CageSetupUnpack)"
  ${EndIf}

  ; Install the bundled offline runtime only when WebView2 is absent.
  !insertmacro InstallPhase "detecting_webview_runtime"
  SetRegView 32
  ReadRegStr $0 HKLM "SOFTWARE\Microsoft\EdgeUpdate\Clients\${WEBVIEW2_APP_ID}" "pv"
  ReadRegStr $1 HKCU "Software\Microsoft\EdgeUpdate\Clients\${WEBVIEW2_APP_ID}" "pv"
  SetRegView 64
  ${If} $0 == ""
  ${OrIf} $0 == "0.0.0.0"
    ${If} $1 == ""
    ${OrIf} $1 == "0.0.0.0"
      DetailPrint "$(CageSetupWebView)"
      !insertmacro InstallPhase "installing_webview_runtime"
      ExecWait '"$StageDir\resources\runtime\MicrosoftEdgeWebView2RuntimeInstallerX64.exe" /silent /install' $0
      StrCpy $HelperExit $0
      Call WriteInstallDiagnostic
      ${If} $0 != 0
        SetOutPath "$TEMP"
        RMDir /r "$StageDir"
        SetErrorLevel 2
        Abort "$(CageSetupWebViewError)"
      ${EndIf}
    ${EndIf}
  ${EndIf}

  SetOutPath "$TEMP"
  StrCpy $BackupDir ""
  ${If} $ExistingInstall == "1"
    ${GetParent} "$INSTDIR" $0
    GetTempFileName $BackupDir "$0"
    Delete "$BackupDir"
  ${EndIf}
  InitPluginsDir
  SetOutPath "$PLUGINSDIR"
  File /oname=installation-files.ps1 "installation-files.ps1"
  File /oname=verified-file-deletion.cs "verified-file-deletion.cs"
  DetailPrint "$(CageSetupDetail6)"
  !insertmacro InstallPhase "preparing_file_transaction"
  ; CageData is the default storage of a packaged Windows installation. It
  ; stays in place: a VM registered on it keeps its paths and open handles.
  nsExec::ExecToStack '"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -ExecutionPolicy Bypass -File "$PLUGINSDIR\installation-files.ps1" -Operation prepare -Target "$StageDir" -Source "$INSTDIR" -Backup "$BackupDir" -Receipt "$TransactionRecord" -KeepInPlace CageData $DiagnosticArguments'
  Pop $0
  Pop $1
  StrCpy $HelperExit $0
  Call WriteInstallDiagnostic
  DetailPrint "$1"
  ${If} $0 != 0
    SetOutPath "$TEMP"
    RMDir /r "$StageDir"
    SetErrorLevel 2
    Abort "$(CageSetupDetail7)"
  ${EndIf}
  SetOutPath "$TEMP"
  ${If} $ExistingInstall == "1"
    !insertmacro InstallPhase "moving_previous_application"
    ; The installation folder itself stays; only its program and personal
    ; entries move, so kept data and open handles below it are untouched.
    nsExec::ExecToStack '"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -ExecutionPolicy Bypass -File "$PLUGINSDIR\installation-files.ps1" -Operation detach -Target "$BackupDir" -Source "$INSTDIR" -Receipt "$TransactionRecord" $DiagnosticArguments'
    Pop $0
    Pop $1
    StrCpy $HelperExit $0
    Call WriteInstallDiagnostic
    ${If} $0 != 0
      DetailPrint "$1"
      nsExec::ExecToStack '"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -ExecutionPolicy Bypass -File "$PLUGINSDIR\installation-files.ps1" -Operation complete -Target "$INSTDIR" -Receipt "$TransactionRecord" $DiagnosticArguments'
      Pop $0
      Pop $1
      RMDir /r "$StageDir"
      SetErrorLevel 2
      Abort "$(CageSetupBusyFiles)"
    ${EndIf}
    DetailPrint "$(CageSetupDetail8)"
    !insertmacro InstallPhase "moving_personal_files"
    nsExec::ExecToStack '"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -ExecutionPolicy Bypass -File "$PLUGINSDIR\installation-files.ps1" -Operation move -Target "$StageDir" -Source "$BackupDir" -Receipt "$TransactionRecord" $DiagnosticArguments'
    Pop $0
    Pop $1
    StrCpy $HelperExit $0
    Call WriteInstallDiagnostic
    DetailPrint "$1"
    ${If} $0 != 0
      Call RestorePreservedFiles
      RMDir /r "$StageDir"
      SetErrorLevel 2
      Abort "$(CageSetupDetail9)"
    ${EndIf}
  ${Else}
    RMDir "$INSTDIR"
  ${EndIf}
  !insertmacro InstallPhase "publishing_application"
  ClearErrors
  ${If} $BackupDir != ""
    ; The kept installation folder receives the staged entries one by one.
    nsExec::ExecToStack '"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -ExecutionPolicy Bypass -File "$PLUGINSDIR\installation-files.ps1" -Operation attach -Target "$INSTDIR" -Source "$StageDir" $DiagnosticArguments'
    Pop $0
    Pop $1
    StrCpy $HelperExit $0
    Call WriteInstallDiagnostic
    ${If} $0 != 0
      DetailPrint "$1"
      SetErrors
    ${EndIf}
  ${Else}
    ; Security software can briefly hold files just extracted to the staging
    ; folder, which denies renaming it. Retry for up to ten seconds and record
    ; the last Win32 error in the diagnostic instead of the previous helper's.
    StrCpy $2 0
    publish_retry:
    System::Call 'kernel32::MoveFileExW(w "$StageDir", w "$INSTDIR", i 0) i .r0 ?e'
    Pop $1
    ${If} $0 == 0
      IntOp $2 $2 + 1
      ${If} $2 < 20
        Sleep 500
        Goto publish_retry
      ${EndIf}
      StrCpy $HelperExit "win32:$1"
      Call WriteInstallDiagnostic
      SetErrors
    ${EndIf}
  ${EndIf}
  ${If} ${Errors}
    ${If} $BackupDir != ""
      Call RestorePreservedFiles
    ${EndIf}
    RMDir /r "$StageDir"
    ${If} $BackupDir == ""
      ; A first installation has nothing to restore: remove its receipt so a
      ; retry is not refused. The helper keeps it if any entry was involved.
      nsExec::ExecToStack '"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -ExecutionPolicy Bypass -File "$PLUGINSDIR\installation-files.ps1" -Operation abandon -Target "$INSTDIR" -Receipt "$TransactionRecord" $DiagnosticArguments'
      Pop $0
      Pop $1
    ${EndIf}
    SetErrorLevel 2
    Abort "$(CageSetupRestore)"
  ${EndIf}
  ${If} $BackupDir != ""
    !insertmacro InstallPhase "cleaning_previous_application"
    DetailPrint "$(CageSetupDetail10)"
    nsExec::ExecToStack '"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -ExecutionPolicy Bypass -File "$PLUGINSDIR\installation-files.ps1" -Operation cleanup -Target "$INSTDIR" -Source "$BackupDir" -Receipt "$TransactionRecord" $DiagnosticArguments'
    Pop $0
    Pop $1
    StrCpy $HelperExit $0
    Call WriteInstallDiagnostic
    DetailPrint "$1"
    ${If} $0 != 0
      DetailPrint "$(CageSetupDetail11)"
      SetErrorLevel 2
      Abort "$(CageSetupDetail12)"
    ${EndIf}
  ${Else}
    !insertmacro InstallPhase "completing_file_transaction"
    nsExec::ExecToStack '"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -ExecutionPolicy Bypass -File "$PLUGINSDIR\installation-files.ps1" -Operation complete -Target "$INSTDIR" -Receipt "$TransactionRecord" $DiagnosticArguments'
    Pop $0
    Pop $1
    StrCpy $HelperExit $0
    Call WriteInstallDiagnostic
    ${If} $0 != 0
      SetErrorLevel 2
      Abort "$(CageSetupDetail13)"
    ${EndIf}
  ${EndIf}
  !insertmacro InstallPhase "registering_installation"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayName" "Cage"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "Publisher" "Cage contributors"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "UninstallString" '$\"$INSTDIR\Uninstall.exe$\"'
  WriteRegStr HKCU "${UNINSTALL_KEY}" "QuietUninstallString" '$\"$INSTDIR\Uninstall.exe$\" /S'
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayIcon" "$INSTDIR\Cage.exe,0"
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "NoRepair" 1
  SetOutPath "$INSTDIR"
  ${If} $DesktopShortcut == 1
    CreateShortcut "$DESKTOP\Cage.lnk" "$INSTDIR\Cage.exe"
  ${Else}
    Delete "$DESKTOP\Cage.lnk"
  ${EndIf}
  ${If} $StartMenuShortcuts == 1
    CreateDirectory "$SMPROGRAMS\Cage"
    CreateShortcut "$SMPROGRAMS\Cage\Cage.lnk" "$INSTDIR\Cage.exe"
    CreateShortcut "$SMPROGRAMS\Cage\Uninstall Cage.lnk" "$INSTDIR\Uninstall.exe"
  ${Else}
    Delete "$SMPROGRAMS\Cage\Cage.lnk"
    Delete "$SMPROGRAMS\Cage\Uninstall Cage.lnk"
    RMDir "$SMPROGRAMS\Cage"
  ${EndIf}
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "DesktopShortcut" $DesktopShortcut
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "StartMenuShortcuts" $StartMenuShortcuts
  ${If} $LANGUAGE != $InstallerSystemLanguage
    ; Seed a fresh profile after a successful interactive installation.
    nsExec::ExecToLog /TIMEOUT=10000 '"$INSTDIR\resources\cage.exe" interface-language --seed-windows-id "$LANGUAGE"'
    Pop $0
  ${EndIf}
  !insertmacro InstallPhase "completed"
  SetErrorLevel 0
SectionEnd

Function un.onInit
  !insertmacro CageSelectInstallerLanguage
  StrCpy $InstallOutcome "running"
  StrCpy $HelperExit ""
  StrCpy $DiagnosticArguments ""
  ${GetParameters} $0
  ${GetOptions} $0 "/DIAGNOSTICS=" $DiagnosticFile
  ${If} $DiagnosticFile != ""
    StrCpy $DiagnosticArguments '-Diagnostics "$DiagnosticFile.helper.json"'
  ${EndIf}
  !insertmacro UninstallPhase "checking_installation_identity"
  SetShellVarContext current
  SetRegView 64
  FileOpen $0 "$INSTDIR\Cage.install-id" r
  FileRead $0 $1
  FileClose $0
  ${If} $1 != "${APP_ID}"
    StrCpy $HelperExit "identity_mismatch"
    Call un.WriteInstallDiagnostic
    SetErrorLevel 2
    Abort "$(CageSetupIdentity)"
  ${EndIf}
  !insertmacro UninstallPhase "checking_stopped_processes"
  !insertmacro CheckStopped "Cage.exe"
  !insertmacro CheckStopped "resources\cage.exe"
  !insertmacro CheckStopped "resources\mihomo.exe"
  !insertmacro CheckStopped "resources\runtime\qemu\qemu-system-x86_64.exe"
FunctionEnd

Section "Uninstall"
  !insertmacro UninstallPhase "acquiring_installation_lock"
  !insertmacro AcquireInstallationMutex
  !insertmacro UninstallPhase "checking_pending_transaction"
  ${If} ${FileExists} "$INSTDIR.cage-installation.json"
    StrCpy $HelperExit "pending_install_transaction"
    Call un.WriteInstallDiagnostic
    SetErrorLevel 2
    Abort "$(CageSetupDetail14)"
  ${EndIf}
  InitPluginsDir
  SetOutPath "$PLUGINSDIR"
  File /oname=installation-files.ps1 "installation-files.ps1"
  File /oname=verified-file-deletion.cs "verified-file-deletion.cs"
  File /oname=install-files.json "${FILE_MANIFEST}"
  ; PowerShell 5.1 Add-Type must not resolve NSIS's native System.dll as a
  ; managed compiler reference from the plugin working directory.
  SetOutPath "$TEMP"
  !insertmacro UninstallPhase "removing_owned_files"
  nsExec::ExecToStack '"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -ExecutionPolicy Bypass -File "$PLUGINSDIR\installation-files.ps1" -Operation uninstall -Target "$INSTDIR" -Manifest "$PLUGINSDIR\install-files.json" $DiagnosticArguments'
  Pop $0
  Pop $1
  StrCpy $HelperExit $0
  Call un.WriteInstallDiagnostic
  DetailPrint "$1"
  ${If} $0 != 0
    SetErrorLevel 2
    Abort "$(CageSetupDetail15)"
  ${EndIf}
  !insertmacro UninstallPhase "removing_shortcuts_and_registration"
  Delete "$DESKTOP\Cage.lnk"
  Delete "$SMPROGRAMS\Cage\Cage.lnk"
  Delete "$SMPROGRAMS\Cage\Uninstall Cage.lnk"
  RMDir "$SMPROGRAMS\Cage"
  DeleteRegKey HKCU "${UNINSTALL_KEY}"
  !insertmacro UninstallPhase "removing_install_directory"
  RMDir "$INSTDIR"
SectionEnd
