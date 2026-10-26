@echo off
chcp 65001 >nul
rem 用部署内探测到的 Python 运行示例脚本：优先 WB2A_PYTHON / runtime\python，再退回 PATH。
set PYTHONUTF8=1
set PYTHONIOENCODING=utf-8
set "PYEXE=%WB2A_PYTHON%"
if not defined PYEXE if exist "%~dp0..\runtime\python\python.exe" set "PYEXE=%~dp0..\runtime\python\python.exe"
if not defined PYEXE if exist "%~dp0..\runtime\venv\Scripts\python.exe" set "PYEXE=%~dp0..\runtime\venv\Scripts\python.exe"
if not defined PYEXE (
  for /f "delims=" %%P in ('where python.exe 2^>nul') do if not defined PYEXE set "PYEXE=%%P"
)
if not defined PYEXE (
  echo 未找到 Python 3，请先运行部署根目录的一键部署.cmd。
  pause
  exit /b 1
)
"%PYEXE%" "%~dp0api-example.py" --interactive
pause
