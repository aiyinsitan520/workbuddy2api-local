param([switch]$Uninstall)
# 安装 / 卸载 workbuddy2api 看门狗计划任务
#   安装：  powershell -NoProfile -ExecutionPolicy Bypass -File watchdog-install.ps1
#   卸载：  powershell -NoProfile -ExecutionPolicy Bypass -File watchdog-install.ps1 -Uninstall
$ErrorActionPreference = 'Stop'
$root = $PSScriptRoot
$taskName = 'workbuddy2api-watchdog'
$watchdog = Join-Path $root 'watchdog.ps1'
$supervisorTaskName = 'workbuddy2api-watchdog-supervisor'
$supervisor = Join-Path $root 'watchdog-supervisor.ps1'

if ($Uninstall) {
    $existing = Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
    if ($existing) { Unregister-ScheduledTask -TaskName $taskName -Confirm:$false }
    $existingSupervisor = Get-ScheduledTask -TaskName $supervisorTaskName -ErrorAction SilentlyContinue
    if ($existingSupervisor) { Unregister-ScheduledTask -TaskName $supervisorTaskName -Confirm:$false }
    Write-Host "已卸载计划任务：$taskName、$supervisorTaskName"
    return
}

if (-not (Test-Path -LiteralPath $watchdog)) { throw "找不到 watchdog.ps1：$watchdog" }
if (-not (Test-Path -LiteralPath $supervisor)) { throw "找不到 watchdog-supervisor.ps1：$supervisor" }

# 必须经由 wscript.exe 转调：Win11 默认终端为 Windows Terminal 时，
# 任务计划程序直接启动 powershell.exe 即使带 -WindowStyle Hidden 也会闪出终端窗口。
$wscript = Join-Path $env:SystemRoot 'System32\wscript.exe'
$launcher = Join-Path $root 'watchdog-launch.vbs'
if (-not (Test-Path -LiteralPath $launcher)) { throw "找不到 watchdog-launch.vbs：$launcher" }

$action = New-ScheduledTaskAction -Execute $wscript `
    -Argument ('"{0}" "{1}"' -f $launcher, $watchdog) `
    -WorkingDirectory $root

# 登录时启动一次，之后每 1 分钟重复一次（无限期）
$logon = New-ScheduledTaskTrigger -AtLogOn -User ([System.Security.Principal.WindowsIdentity]::GetCurrent().Name)
$interval = New-ScheduledTaskTrigger -Once -At (Get-Date).AddMinutes(1) `
    -RepetitionInterval (New-TimeSpan -Minutes 1) `
    -RepetitionDuration (New-TimeSpan -Days 3650)

$settings = New-ScheduledTaskSettingsSet `
    -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
    -StartWhenAvailable -MultipleInstances IgnoreNew `
    -ExecutionTimeLimit (New-TimeSpan -Minutes 5)

Register-ScheduledTask -TaskName $taskName -Action $action -Trigger @($logon,$interval) `
    -Settings $settings -Description 'workbuddy2api 服务看门狗：检查 7863/7864/8787，掉线自动拉起' `
    -Force | Out-Null

$supervisorAction = New-ScheduledTaskAction -Execute $wscript `
    -Argument ('"{0}" "{1}"' -f $launcher, $supervisor) `
    -WorkingDirectory $root
$supervisorLogon = New-ScheduledTaskTrigger -AtLogOn -User ([System.Security.Principal.WindowsIdentity]::GetCurrent().Name)
$supervisorInterval = New-ScheduledTaskTrigger -Once -At (Get-Date).AddMinutes(1) `
    -RepetitionInterval (New-TimeSpan -Minutes 1) `
    -RepetitionDuration (New-TimeSpan -Days 3650)
Register-ScheduledTask -TaskName $supervisorTaskName -Action $supervisorAction -Trigger @($supervisorLogon,$supervisorInterval) `
    -Settings $settings -Description 'workbuddy2api 看门狗监督器：检查主看门狗心跳并告警' `
    -Force | Out-Null

Write-Host "已安装计划任务：$taskName、$supervisorTaskName（登录后每 1 分钟检查一次）"
Write-Host '临时停用：新建空文件 data\watchdog-pause；删除该文件即恢复。'
