; Tauri keeps its installer, shortcuts, deep links and sidecar. Extend only
; the current user's PATH so new terminals/agents can run agentnet.exe.
!define AGENTNET_HOOK_DIR "${__FILEDIR__}"

!macro NSIS_HOOK_POSTINSTALL
  File "/oname=agentnet-path.ps1" "${AGENTNET_HOOK_DIR}\path.ps1"
  nsExec::ExecToStack '"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "$INSTDIR\agentnet-path.ps1" -Action add -InstallDir "$INSTDIR"'
  Pop $0
  Pop $1
  ${If} $0 != 0
    DetailPrint "AgentNet user PATH update failed: $1"
    MessageBox MB_ICONEXCLAMATION "AgentNet installed, but the agentnet command could not be added to your user PATH." /SD IDOK
  ${EndIf}
  System::Call 'user32::SendMessageTimeoutW(p 0xffff, i 0x1a, p 0, w "Environment", i 2, i 5000, *p .r0)'
!macroend

!macro NSIS_HOOK_PREUNINSTALL
  nsExec::ExecToStack '"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "$INSTDIR\agentnet-path.ps1" -Action remove -InstallDir "$INSTDIR"'
  Pop $0
  Pop $1
  ${If} $0 != 0
    DetailPrint "AgentNet user PATH cleanup failed: $1"
  ${EndIf}
  Delete "$INSTDIR\agentnet-path.ps1"
  System::Call 'user32::SendMessageTimeoutW(p 0xffff, i 0x1a, p 0, w "Environment", i 2, i 5000, *p .r0)'
!macroend
