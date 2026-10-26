param([switch]$NoLogin)
. (Join-Path $PSScriptRoot 'common.ps1')
if (-not (Test-Path -LiteralPath $script:ServerExe)) { throw '缺少 wb2api.exe，请先运行 build.ps1。' }
# 主动启动即恢复看门狗监控（stop.ps1 会写入 data\watchdog-pause）
Remove-Item -LiteralPath (Join-Path $script:DeploymentRoot 'data\watchdog-pause') -Force -ErrorAction SilentlyContinue
$serverProcess = Get-ManagedServer
if (-not $serverProcess) {
    $listenPort = [int]($script:Config.listen.Split(':')[-1])
    $listener = Get-NetTCPConnection -State Listen -LocalPort $listenPort -ErrorAction SilentlyContinue
    if ($listener) { throw "端口 $listenPort 已被其他进程占用，请检查后再启动。" }
    $serverProcess = Start-Process -FilePath $script:ServerExe -ArgumentList '-config', 'config.json' -WorkingDirectory $script:DeploymentRoot -WindowStyle Hidden -RedirectStandardOutput (Join-Path $script:DeploymentRoot 'logs\server.stdout.log') -RedirectStandardError (Join-Path $script:DeploymentRoot 'logs\server.stderr.log') -PassThru
    [IO.File]::WriteAllText($script:PidFile, [string]$serverProcess.Id)
}
$health = $null
for ($attempt = 0; $attempt -lt 20; $attempt++) {
    $serverProcess.Refresh()
    if ($serverProcess.HasExited) { throw '服务启动失败，请查看 logs\server.stderr.log。' }
    $health = Get-GatewayHealth
    if ($health -and $health.service -eq 'workbuddy2api') { break }
    Start-Sleep -Milliseconds 500
}
if (-not $health -or $health.service -ne 'workbuddy2api') { throw '服务没有通过启动检查，请查看日志。' }
Write-Host "服务已启动，API Base URL：$script:BaseUrl/v1"
Write-Host 'API Key 保存在 connection.txt 和 config.json。'
if ($health.total -eq 0) {
    Write-Host '尚未添加账号；登录完成后才能进行模型对话。'
    if (-not $NoLogin) { & (Join-Path $PSScriptRoot 'login.ps1') }
} else {
    Write-Host "账号数：$($health.total)，可用账号数：$($health.healthy)"
}

if (Test-Path -LiteralPath (Join-Path $PSScriptRoot 'compat.ps1')) { & (Join-Path $PSScriptRoot 'compat.ps1') }
