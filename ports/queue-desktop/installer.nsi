; The Windows installer for the queue manager's tray app. package.sh builds it:
;
;   makensis -DVERSION=1.2.3 -DNUMERIC=1.2.3 -DEXE=<built exe> -DOUTFILE=<setup.exe> installer.nsi
;
; It installs for every user, so it can add the firewall rule that lets phones
; on a private network reach the API on port 8177.

Unicode true
ManifestDPIAware true
RequestExecutionLevel admin

!define PRODUCT "CK Queue Manager"
!define COMPANY "rrgmc"
!define EXE_NAME "ck-queue-manager-tray.exe"
!define UNINSTALL_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\CKQueueManager"

!ifndef VERSION | NUMERIC | EXE | OUTFILE
  !error "VERSION, NUMERIC, EXE and OUTFILE must be defined"
!endif

Name "${PRODUCT}"
OutFile "${OUTFILE}"
InstallDir "$PROGRAMFILES64\${COMPANY}\${PRODUCT}"
InstallDirRegKey HKLM "${UNINSTALL_KEY}" "InstallLocation"
ShowInstDetails show

VIProductVersion "${NUMERIC}.0"
VIFileVersion "${NUMERIC}.0"
VIAddVersionKey "CompanyName" "${COMPANY}"
VIAddVersionKey "FileDescription" "${PRODUCT} Installer"
VIAddVersionKey "ProductName" "${PRODUCT}"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "FileVersion" "${NUMERIC}"

!include "MUI2.nsh"
!include "x64.nsh"

!define MUI_ICON "..\desktop\build\windows\icon.ico"
!define MUI_UNICON "..\desktop\build\windows\icon.ico"
!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_RUN "$INSTDIR\${EXE_NAME}"
!define MUI_FINISHPAGE_RUN_TEXT "Start ${PRODUCT} now"

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_COMPONENTS
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"
!insertmacro MUI_LANGUAGE "PortugueseBR"

Function .onInit
  ${IfNot} ${RunningX64}
    MessageBox MB_ICONSTOP "${PRODUCT} needs 64-bit Windows."
    Abort
  ${EndIf}
  SetRegView 64
FunctionEnd

Function un.onInit
  SetRegView 64
FunctionEnd

; A running copy holds its files. Asked first to close, it stops the queue
; manager and closes the unit's session cleanly; only a copy that does not
; close in time is ended.
!macro StopRunning
  nsExec::Exec 'taskkill /IM "${EXE_NAME}"'
  Sleep 3000
  nsExec::Exec 'taskkill /F /IM "${EXE_NAME}"'
!macroend

Section "${PRODUCT}" SecMain
  SectionIn RO
  SetShellVarContext all
  !insertmacro StopRunning

  SetOutPath "$INSTDIR"
  File "/oname=${EXE_NAME}" "${EXE}"
  File "/oname=icon.ico" "..\desktop\build\windows\icon.ico"

  CreateShortCut "$SMPROGRAMS\${PRODUCT}.lnk" "$INSTDIR\${EXE_NAME}" "" "$INSTDIR\icon.ico"

  ; Phones on the Wi-Fi reach the API on port 8177. Private networks only.
  nsExec::Exec 'netsh advfirewall firewall delete rule name="${PRODUCT}"'
  nsExec::ExecToLog 'netsh advfirewall firewall add rule name="${PRODUCT}" dir=in action=allow program="$INSTDIR\${EXE_NAME}" enable=yes profile=private'

  WriteUninstaller "$INSTDIR\uninstall.exe"
  WriteRegStr HKLM "${UNINSTALL_KEY}" "DisplayName" "${PRODUCT}"
  WriteRegStr HKLM "${UNINSTALL_KEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKLM "${UNINSTALL_KEY}" "Publisher" "${COMPANY}"
  WriteRegStr HKLM "${UNINSTALL_KEY}" "DisplayIcon" "$INSTDIR\icon.ico"
  WriteRegStr HKLM "${UNINSTALL_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKLM "${UNINSTALL_KEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegStr HKLM "${UNINSTALL_KEY}" "URLInfoAbout" "https://github.com/rrgmc/ck-karaoke"
  WriteRegDWORD HKLM "${UNINSTALL_KEY}" "NoModify" 1
  WriteRegDWORD HKLM "${UNINSTALL_KEY}" "NoRepair" 1
  WriteRegDWORD HKLM "${UNINSTALL_KEY}" "EstimatedSize" 25000
SectionEnd

Section "Desktop shortcut" SecDesktop
  SetShellVarContext all
  CreateShortCut "$DESKTOP\${PRODUCT}.lnk" "$INSTDIR\${EXE_NAME}" "" "$INSTDIR\icon.ico"
SectionEnd

; Off by default: a queue manager is usually started for an evening.
Section /o "Start with Windows" SecStartup
  SetShellVarContext all
  CreateShortCut "$SMSTARTUP\${PRODUCT}.lnk" "$INSTDIR\${EXE_NAME}" "" "$INSTDIR\icon.ico"
SectionEnd

Section "uninstall"
  SetShellVarContext all
  !insertmacro StopRunning
  nsExec::Exec 'netsh advfirewall firewall delete rule name="${PRODUCT}"'
  Delete "$SMPROGRAMS\${PRODUCT}.lnk"
  Delete "$DESKTOP\${PRODUCT}.lnk"
  Delete "$SMSTARTUP\${PRODUCT}.lnk"
  Delete "$INSTDIR\${EXE_NAME}"
  Delete "$INSTDIR\icon.ico"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"
  RMDir "$PROGRAMFILES64\${COMPANY}"
  DeleteRegKey HKLM "${UNINSTALL_KEY}"
  ; The settings, the catalogue and the kept evening stay in the user's
  ; configuration directory, so a reinstall keeps them.
SectionEnd

LangString DESC_SecMain ${LANG_ENGLISH} "The queue manager and its Start menu entry, and a firewall rule for private networks."
LangString DESC_SecMain ${LANG_PORTUGUESEBR} "O gerenciador de fila e sua entrada no menu Iniciar, e uma regra de firewall para redes privadas."
LangString DESC_SecDesktop ${LANG_ENGLISH} "A shortcut on the desktop."
LangString DESC_SecDesktop ${LANG_PORTUGUESEBR} "Um atalho na área de trabalho."
LangString DESC_SecStartup ${LANG_ENGLISH} "Start the queue manager when you sign in to Windows."
LangString DESC_SecStartup ${LANG_PORTUGUESEBR} "Iniciar o gerenciador de fila ao entrar no Windows."

!insertmacro MUI_FUNCTION_DESCRIPTION_BEGIN
  !insertmacro MUI_DESCRIPTION_TEXT ${SecMain} $(DESC_SecMain)
  !insertmacro MUI_DESCRIPTION_TEXT ${SecDesktop} $(DESC_SecDesktop)
  !insertmacro MUI_DESCRIPTION_TEXT ${SecStartup} $(DESC_SecStartup)
!insertmacro MUI_FUNCTION_DESCRIPTION_END
