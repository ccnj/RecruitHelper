; RecruitHelper Windows installer.
;
; THIS FILE MUST STAY PURE ASCII -- comments included.
; The macOS build of makensis is an ANSI-only build: a single non-ASCII byte
; anywhere in the script aborts with "Bad text encoding: line 1", and neither
; -INPUTCHARSET UTF8 nor a UTF-8 BOM changes that. Product UI text stays
; Chinese as usual; only this installer is limited to the program identifier
; "RecruitHelper". Do not "fix" the comments back to Chinese -- it will not build.
;
; Why not electron-builder's nsis target: it compiles the installer and then has
; to RUN that 32-bit exe to extract the uninstaller. On Apple Silicon that needs
; wine, and qemu cannot run 32-bit Windows binaries (Rosetta does not cover
; 32-bit either). Plain NSIS writes the uninstaller at compile time via
; WriteUninstaller, running nothing -- so a native makensis is all we need,
; and the whole build chain drops Docker and wine.
;
; Invoked by scripts/build-win.sh, which passes the paths and version via -D.

; No "Unicode true": the macOS makensis build aborts with std::bad_alloc on it.
; The installer is therefore ANSI, which is fine here -- this script is pure
; ASCII, and on a Chinese Windows the ANSI code page is GBK, so a user profile
; path like C:\Users\<Chinese name>\ still resolves correctly.
SetCompressor /SOLID lzma

!ifndef SOURCE_DIR
  !error "missing -DSOURCE_DIR"
!endif
!ifndef OUT_FILE
  !error "missing -DOUT_FILE"
!endif
!ifndef VERSION
  !define VERSION "0.0.0"
!endif

!define APP_NAME "RecruitHelper"
!define APP_EXE "RecruitHelper.exe"
!define BRAIN_EXE "RecruitHelperBrain.exe"
!define UNINST_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\RecruitHelper"

Name "${APP_NAME}"
OutFile "${OUT_FILE}"
; Per-user install: no admin rights, therefore no UAC prompt.
InstallDir "$LOCALAPPDATA\Programs\RecruitHelper"
RequestExecutionLevel user
ShowInstDetails show
ShowUninstDetails show

; No MUI2: an internal tool does not need wizard polish, and the classic pages
; have the fewest dependencies and the most predictable behaviour.
Page instfiles
UninstPage uninstConfirm
UninstPage instfiles

; On upgrade, a still-running process holding files in the install directory
; makes the overwrite fail. A normal shell exit already stops the brain
; (before-quit in main.js); this covers the orphan brain left behind when the
; shell was killed instead.
;
; Once the brain is terminated, in-flight WAL entries, unsettled commands and
; suspect decisions all converge through the existing recovery path on next
; start. The installer opens no side channel for that and does not attempt to
; "gracefully wind down business" -- that is the brain's own ledger discipline.
!macro StopRunningApp
  DetailPrint "Stopping running RecruitHelper processes..."
  ; The brain binary is deliberately not named service.exe, so this kill cannot
  ; hit unrelated same-named processes on the system.
  nsExec::Exec 'taskkill /F /IM "${BRAIN_EXE}" /T'
  Pop $0
  nsExec::Exec 'taskkill /F /IM "${APP_EXE}" /T'
  Pop $0
  ; A non-zero result just means the process was not running. Not an error.
  Sleep 800
!macroend

Section "Install"
  !insertmacro StopRunningApp

  ; Clear the old files before overwriting so nothing from the previous version
  ; survives into the new one. Safe: the install directory holds no user data
  ; (business DB lives in %APPDATA%, the plugin in %LOCALAPPDATA%).
  RMDir /r "$INSTDIR"
  SetOutPath "$INSTDIR"
  ; Forward slashes: this is a compile-time host path, resolved by makensis on
  ; macOS. Runtime paths below ($INSTDIR etc.) stay backslashed for Windows.
  File /r "${SOURCE_DIR}/*.*"

  WriteUninstaller "$INSTDIR\Uninstall.exe"

  CreateShortcut "$DESKTOP\${APP_NAME}.lnk" "$INSTDIR\${APP_EXE}"
  CreateDirectory "$SMPROGRAMS\${APP_NAME}"
  CreateShortcut "$SMPROGRAMS\${APP_NAME}\${APP_NAME}.lnk" "$INSTDIR\${APP_EXE}"
  CreateShortcut "$SMPROGRAMS\${APP_NAME}\Uninstall ${APP_NAME}.lnk" "$INSTDIR\Uninstall.exe"

  ; Per-user install writes HKCU so the entry shows up in Apps & Features.
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayName" "${APP_NAME}"
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINST_KEY}" "Publisher" "aliyutaozi"
  WriteRegStr HKCU "${UNINST_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayIcon" "$INSTDIR\${APP_EXE}"
  WriteRegStr HKCU "${UNINST_KEY}" "UninstallString" '"$INSTDIR\Uninstall.exe"'
  WriteRegDWORD HKCU "${UNINST_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINST_KEY}" "NoRepair" 1

  DetailPrint "Installed. Still required on first run: load the plugin in Chrome,"
  DetailPrint "activate this machine, and configure the model connection."
SectionEnd

Section "Uninstall"
  !insertmacro StopRunningApp

  Delete "$DESKTOP\${APP_NAME}.lnk"
  RMDir /r "$SMPROGRAMS\${APP_NAME}"
  RMDir /r "$INSTDIR"
  DeleteRegKey HKCU "${UNINST_KEY}"

  ; Deliberately keeps the business data (%APPDATA%\RecruitHelper) and the
  ; plugin directory (%LOCALAPPDATA%\RecruitHelper\plugin): an uninstaller must
  ; not destroy business facts, and wiping the plugin directory would leave
  ; Chrome reporting a corrupt extension.
  DetailPrint "Program removed. Business data and the plugin directory were kept."
SectionEnd
