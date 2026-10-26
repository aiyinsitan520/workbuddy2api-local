@echo off
chcp 65001 >nul
"%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -ExecutionPolicy Bypass -File "%~dp0control.ps1" -Action CopyKey %*
if errorlevel 1 (
  echo.
  echo 操作失败，请查看上方提示。
  pause
  exit /b 1
)
pause
