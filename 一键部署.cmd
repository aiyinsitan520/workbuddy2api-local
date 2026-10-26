@echo off
chcp 65001 >nul
title WorkBuddy2API 一键部署
cd /d "%~dp0"

echo ============================================================
echo   WorkBuddy2API 本地版 - 一键部署
echo ============================================================
echo.
echo   将自动完成：
echo     - 准备便携 Go 与 Python 环境
echo     - 生成随机 API Key 与 config.json
echo     - 编译网关程序
echo     - 安装看门狗（掉线自动拉起）
echo.
echo   无需管理员权限，不修改系统环境变量。
echo.
pause

powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0install.ps1" %*
set "RC=%ERRORLEVEL%"

echo.
if not "%RC%"=="0" (
  echo ============================================================
  echo   安装未完成，请查看上方红色提示。
  echo ============================================================
  pause
  exit /b %RC%
)

echo 按任意键退出。
pause >nul
exit /b 0
