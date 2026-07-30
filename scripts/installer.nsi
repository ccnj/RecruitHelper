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
; Executable names come from build-win.sh, which reads them from the single
; sources of truth (layout.js for the brain, package.json for the shell).
; Hardcoding them here would let a rename drift: the kill below would silently
; miss the real process and the upgrade overwrite would fail.
!ifndef APP_EXE
  !error "missing -DAPP_EXE"
!endif
!ifndef BRAIN_EXE
  !error "missing -DBRAIN_EXE"
!endif
; Path to assets/app-icon.ico, passed in by build-win.sh. Used for the installer's
; own icon and for the shortcuts. The exe's own icon cannot be set here -- that
; needs rcedit to rewrite PE resources, and rcedit is a 32-bit Windows binary that
; Apple Silicon cannot run.
!ifndef ICON_FILE
  !error "missing -DICON_FILE"
!endif
!define UNINST_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\RecruitHelper"

Name "${APP_NAME}"
OutFile "${OUT_FILE}"
Icon "${ICON_FILE}"
UninstallIcon "${ICON_FILE}"
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
; Appends one line to a log that survives the install, so a failed silent run
; leaves evidence. It must NOT live under $INSTDIR: that directory gets wiped by
; RMDir /r a few lines below, which would erase exactly the record needed to
; explain a failure at that point. The updates directory is the client's own
; staging area and is never touched by the installer.
!macro InstallLog Text
  CreateDirectory "$LOCALAPPDATA\RecruitHelper\updates"
  ; The NSIS error flag is only ever SET by failing instructions -- successful
  ; ones never clear it. Without ClearErrors, a stray flag from an unrelated
  ; earlier failure (RMDir hitting a file the killed process still holds, a
  ; shortcut that failed to write...) would skip the append below even though
  ; FileOpen succeeded, and leak the handle. The partial-failure runs that set
  ; such flags are exactly the runs this log exists to explain.
  ClearErrors
  FileOpen $8 "$LOCALAPPDATA\RecruitHelper\updates\install.log" a
  ; "+4" counts instructions: FileSeek, FileWrite, FileClose, then the first
  ; instruction after the macro. A plain label is not an option here -- the
  ; macro expands at every insertion point and duplicate labels fail the
  ; build. Renumber this jump whenever the macro body changes.
  IfErrors +4
    FileSeek $8 0 END
    FileWrite $8 "${Text}$\r$\n"
    FileClose $8
!macroend

!macro StopRunningApp
  DetailPrint "Stopping running RecruitHelper processes..."
  ; The brain binary is deliberately not named service.exe, so this kill cannot
  ; hit unrelated same-named processes on the system.
  nsExec::Exec 'taskkill /F /IM "${BRAIN_EXE}" /T'
  Pop $0
  ; NO /T on the shell. During an in-app update the client spawns this installer,
  ; so the installer is a child of ${APP_EXE}; /T walks the process tree by
  ; ParentProcessId and would kill us mid-run. Node's detached:true does not help
  ; -- on Windows it only detaches the console, the parent link remains.
  ;
  ; 2026-07-30 real-machine failure: the update reached "handed off the
  ; installer", the brain died with code=1, and the machine came back still on
  ; the old version with $INSTDIR intact -- the installer never got as far as
  ; overwriting files because it had terminated itself here.
  ;
  ; Dropping /T loses nothing: the only child of the shell that matters is the
  ; brain, already killed by name above.
  nsExec::Exec 'taskkill /F /IM "${APP_EXE}"'
  Pop $0
  ; A non-zero result just means the process was not running. Not an error.
  Sleep 800
!macroend

Section "Install"
  ; $INSTDIR is derived from $LOCALAPPDATA. If that were empty the path would
  ; collapse to \Programs\RecruitHelper on the current drive, and the RMDir /r
  ; below would wipe whatever happens to sit there. Refuse instead.
  StrCmp $LOCALAPPDATA "" 0 +3
    MessageBox MB_ICONSTOP "LOCALAPPDATA is not set; cannot determine a safe install directory."
    Abort

  ; Four breadcrumbs, placed at the boundaries that actually failed or could
  ; fail. A silent run shows nothing on screen, so without these a failure is
  ; only visible as "the version did not change" -- which is what happened on
  ; 2026-07-30 and had to be diagnosed by inference.
  !insertmacro InstallLog "[${VERSION}] installer started"

  !insertmacro StopRunningApp
  !insertmacro InstallLog "[${VERSION}] processes stopped, still alive"

  ; Clear the old files before overwriting so nothing from the previous version
  ; survives into the new one. Safe: the install directory holds no user data
  ; (business DB lives in %APPDATA%, the plugin in %LOCALAPPDATA%).
  RMDir /r "$INSTDIR"
  SetOutPath "$INSTDIR"
  ; "*" rather than "*.*": the latter only matches names containing a dot, so an
  ; extensionless file appearing in a future Electron build would be dropped from
  ; the installer without a word.
  ; Forward slashes: this is a compile-time host path, resolved by makensis on
  ; macOS. Runtime paths below ($INSTDIR etc.) stay backslashed for Windows.
  File /r "${SOURCE_DIR}/*"

  WriteUninstaller "$INSTDIR\Uninstall.exe"

  ; Ship the .ico so the shortcuts can point at it. Without an explicit icon the
  ; shortcuts inherit the exe's, which is still the stock Electron one (see the
  ; rcedit note above), so this is what actually gives the user a branded icon on
  ; the desktop and in the start menu.
  File "${ICON_FILE}"

  CreateShortcut "$DESKTOP\${APP_NAME}.lnk" "$INSTDIR\${APP_EXE}" "" "$INSTDIR\app-icon.ico"
  CreateDirectory "$SMPROGRAMS\${APP_NAME}"
  CreateShortcut "$SMPROGRAMS\${APP_NAME}\${APP_NAME}.lnk" "$INSTDIR\${APP_EXE}" "" "$INSTDIR\app-icon.ico"
  CreateShortcut "$SMPROGRAMS\${APP_NAME}\Uninstall ${APP_NAME}.lnk" "$INSTDIR\Uninstall.exe"

  ; Per-user install writes HKCU so the entry shows up in Apps & Features.
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayName" "${APP_NAME}"
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINST_KEY}" "Publisher" "aliyutaozi"
  WriteRegStr HKCU "${UNINST_KEY}" "InstallLocation" "$INSTDIR"
  ; Point at the .ico rather than the exe: the exe still carries the stock
  ; Electron icon, so this is what makes Apps & Features show the real one.
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayIcon" "$INSTDIR\app-icon.ico"
  WriteRegStr HKCU "${UNINST_KEY}" "UninstallString" '"$INSTDIR\Uninstall.exe"'
  WriteRegDWORD HKCU "${UNINST_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINST_KEY}" "NoRepair" 1

  DetailPrint "Installed. Still required on first run: load the plugin in Chrome,"
  DetailPrint "activate this machine, and configure the model connection."

  ; Silent install means the running client handed us the installer and then quit
  ; on purpose (the in-app "update now" button). Nobody is watching the screen, so
  ; if we do not start the app again the machine is simply left with no client
  ; running and no indication why. An interactive install is the opposite case:
  ; a human is right there and will start it from the shortcut.
  ;
  ; Exec, not ExecWait: waiting would keep the installer process alive for the
  ; whole session, and the client that spawned us is already gone.
  ; A named label, not "+n": relative jumps count *instructions*, and the log
  ; macro below expands to six of them. Any edit inside it would silently move
  ; the target -- and the failure mode of an off-by-one here is "silent install
  ; does not relaunch", i.e. the machine ends up with no client running.
  !insertmacro InstallLog "[${VERSION}] files written"
  IfSilent 0 skip_relaunch
    !insertmacro InstallLog "[${VERSION}] silent install, relaunching client"
    Exec '"$INSTDIR\${APP_EXE}"'
  skip_relaunch:
SectionEnd

Section "Uninstall"
  !insertmacro StopRunningApp

  Delete "$DESKTOP\${APP_NAME}.lnk"
  RMDir /r "$SMPROGRAMS\${APP_NAME}"
  RMDir /r "$INSTDIR"
  DeleteRegKey HKCU "${UNINST_KEY}"

  ; Deliberately keeps the business data (%APPDATA%\recruithelper-client) and the
  ; plugin directory (%LOCALAPPDATA%\RecruitHelper\plugin): an uninstaller must
  ; not destroy business facts, and wiping the plugin directory would leave
  ; Chrome reporting a corrupt extension.
  DetailPrint "Program removed. Business data and the plugin directory were kept."
SectionEnd
