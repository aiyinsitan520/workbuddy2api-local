# workbuddy2api 主看门狗
# 检查进程、端口、账号池和自动拉起结果；每次运行写心跳。
$ErrorActionPreference = 'SilentlyContinue'
$root = $PSScriptRoot
if (-not $root) { $root = Split-Path -Parent $MyInvocation.MyCommand.Path }
$dataDir = Join-Path $root 'data'
$logDir = Join-Path $root 'logs'
$pauseFile = Join-Path $dataDir 'watchdog-pause'
$logFile = Join-Path $logDir 'watchdog.log'
$heartbeatFile = Join-Path $dataDir 'watchdog-heartbeat.json'
$alertStateFile = Join-Path $dataDir 'watchdog-alert-state.json'
$accountStateFile = Join-Path $dataDir 'watchdog-account-warning.json'
New-Item -ItemType Directory -Force -Path $dataDir,$logDir | Out-Null

function Write-WatchdogLog([string]$message) {
    try { Add-Content -LiteralPath $logFile -Encoding UTF8 -Value ('[{0}] {1}' -f (Get-Date -Format 'yyyy-MM-dd HH:mm:ss'), $message) } catch { }
}

function Write-Heartbeat([string]$phase, [bool]$ok, [object[]]$problems) {
    try {
        [pscustomobject]@{
            timestamp = (Get-Date).ToUniversalTime().ToString('o')
            local_time = (Get-Date).ToString('s')
            pid = $PID
            phase = $phase
            ok = $ok
            problems = @($problems)
        } | ConvertTo-Json -Compress | Set-Content -LiteralPath $heartbeatFile -Encoding UTF8
    } catch { Write-WatchdogLog ('heartbeat write failed: {0}' -f $_.Exception.Message) }
}

function Send-Alert([string]$key, [string]$title, [string]$body) {
    $now = Get-Date
    try {
        $old = if (Test-Path $alertStateFile) { Get-Content $alertStateFile -Raw | ConvertFrom-Json } else { $null }
        if ($old -and $old.key -eq $key -and ((New-TimeSpan -Start ([datetime]$old.sent_at) -End $now).TotalHours -lt 6)) { return }
        [pscustomobject]@{ key=$key; sent_at=$now.ToString('o'); title=$title; body=$body } | ConvertTo-Json -Compress | Set-Content $alertStateFile -Encoding UTF8
    } catch { }
    Write-WatchdogLog ('ALERT: {0} | {1}' -f $title, $body)
    try {
        $ps = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
        $xmlTitle = [System.Security.SecurityElement]::Escape($title)
        $xmlBody = [System.Security.SecurityElement]::Escape($body)
        $script = "[Windows.UI.Notifications.ToastNotificationManager,Windows.UI.Notifications,ContentType=WindowsRuntime]>`$null;[Windows.Data.Xml.Dom.XmlDocument,Windows.Data.Xml.Dom.XmlDocument,ContentType=WindowsRuntime]>`$null;`$x=New-Object Windows.Data.Xml.Dom.XmlDocument;`$x.LoadXml('<toast><visual><binding template=`"ToastGeneric`"><text>$xmlTitle</text><text>$xmlBody</text></binding></visual></toast>');[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('WorkBuddy2API').Show((New-Object Windows.UI.Notifications.ToastNotification `$x))"
        Start-Process -FilePath $ps -ArgumentList '-NoProfile','-NonInteractive','-WindowStyle','Hidden','-Command',$script -WindowStyle Hidden | Out-Null
    } catch { Write-WatchdogLog ('toast failed: {0}' -f $_.Exception.Message) }
}

function Write-AccountWarning([string]$key, [string]$body) {
    $now = Get-Date
    try {
        $old = if (Test-Path $accountStateFile) { Get-Content $accountStateFile -Raw | ConvertFrom-Json } else { $null }
        if ($old -and $old.key -eq $key -and ((New-TimeSpan -Start ([datetime]$old.sent_at) -End $now).TotalHours -lt 6)) { return }
        [pscustomobject]@{ key=$key; sent_at=$now.ToString('o'); body=$body } | ConvertTo-Json -Compress | Set-Content $accountStateFile -Encoding UTF8
    } catch { }
    Write-WatchdogLog $body
}

function Test-Url([string]$url, [string]$expect) {
    $curl = Join-Path $env:SystemRoot 'System32\curl.exe'
    $body = & $curl --silent --max-time 4 $url
    return ($LASTEXITCODE -eq 0 -and $body -match $expect)
}

function Get-HealthProblems {
    $problems = @()
    if (-not (Test-Url 'http://127.0.0.1:7863/healthz' 'workbuddy2api')) { $problems += 'gateway:7863' }
    if (-not (Test-Url 'http://127.0.0.1:8787/health' 'unified-account-pool')) { $problems += 'compat:8787' }
    if (-not (Test-Url 'http://127.0.0.1:7864/' 'workbench-token')) { $problems += 'workbench:7864' }
    return @($problems)
}

function Get-AccountProblems {
    $problems = @()
    try {
        $cfg = Get-Content (Join-Path $root 'config.json') -Raw -Encoding UTF8 | ConvertFrom-Json
        $headers = @{ Authorization = 'Bearer ' + $cfg.api_key }
        $status = Invoke-RestMethod -Uri ('http://' + $cfg.listen + '/status') -Headers $headers -TimeoutSec 5
        $disabled = @($status.accounts | Where-Object { $_.disabled })
        # routing_paused 是网关的隔离状态：账号可能尚未被 pool 标成 disabled，
        # 但已经不会参与普通路由。只统计非 disabled 的暂停号，避免重复计数。
        $paused = @($status.accounts | Where-Object { $_.routing_paused -and -not $_.disabled })
        $unavailable = @($disabled + $paused)
        if ($unavailable.Count -gt 0) {
            $names = (($unavailable | Select-Object -First 8 | ForEach-Object {
                if ($_.nickname) { $_.nickname } else { $_.uid.Substring(0,8) }
            }) -join '、')
            if ($disabled.Count -gt 0) { $problems += ('accounts-disabled:{0}' -f $disabled.Count) }
            if ($paused.Count -gt 0) { $problems += ('accounts-routing-paused:{0}' -f $paused.Count) }
            # 账号失效/隔离只写日志和工作台状态，不弹窗；只有看门狗/服务故障才打断桌面。
            $body = 'account warning: {0} 个账号不可用（禁用 {1}，路由暂停 {2}），需要重新登录或重新导入凭据：{3}' -f $unavailable.Count,$disabled.Count,$paused.Count,$names
            Write-AccountWarning ('accounts-unavailable:' + (($unavailable | ForEach-Object { $_.uid }) -join ',')) $body
        }
        foreach ($realm in @('cn','global')) {
            $row = $status.realm_totals.$realm
            if ($row -and [int]$row.total -gt 0 -and [int]$row.healthy -eq 0) {
                $problems += ('realm-unavailable:' + $realm)
                Send-Alert ('realm-unavailable:' + $realm) 'WorkBuddy账号池不可用' ('{0} 版当前没有可用账号，普通请求会失败。' -f $realm)
            }
        }
    } catch { $problems += 'account-status:unreadable' }
    return @($problems)
}

if (Test-Path -LiteralPath $pauseFile) {
    $age = (Get-Date) - (Get-Item -LiteralPath $pauseFile).LastWriteTime
    if ($age.TotalMinutes -lt 5) { Write-Heartbeat 'paused' $true @('paused'); return }
    Remove-Item -LiteralPath $pauseFile -Force -ErrorAction SilentlyContinue
    Write-WatchdogLog 'stale pause file removed'
}

Write-Heartbeat 'checking' $false @()
$problems = @(Get-HealthProblems)
$accountProblems = @(Get-AccountProblems)
if ($problems.Count -eq 0) {
    Write-Heartbeat 'healthy' $true $accountProblems
    return
}

Start-Sleep -Seconds 3
$problems = @(Get-HealthProblems)
if ($problems.Count -eq 0) {
    Write-Heartbeat 'recovered-before-restart' $true $accountProblems
    return
}

Write-WatchdogLog ('unhealthy: {0} -> restarting workbench.ps1' -f ($problems -join ', '))
$powerShell = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
$launchScript = Join-Path $root 'workbench.ps1'
$commandLine = ('"{0}" -NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File "{1}" -NoOpen' -f $powerShell, $launchScript)
$spawned = $false
try {
    $result = ([wmiclass]'Win32_Process').Create($commandLine, $root)
    Write-WatchdogLog ('WMI spawn return={0}' -f $result.ReturnValue)
    $spawned = ($result.ReturnValue -eq 0)
} catch { Write-WatchdogLog ('WMI spawn failed: {0}' -f $_.Exception.Message) }
if (-not $spawned) {
    try {
        Start-Process -FilePath $powerShell -ArgumentList '-NoProfile','-ExecutionPolicy','Bypass','-WindowStyle','Hidden','-File',$launchScript,'-NoOpen' -WorkingDirectory $root -WindowStyle Hidden | Out-Null
        Write-WatchdogLog 'fallback Start-Process spawn launched'
        $spawned = $true
    } catch { Write-WatchdogLog ('fallback spawn failed: {0}' -f $_.Exception.Message) }
}

for ($i=0; $i -lt 20 -and $spawned; $i++) {
    Start-Sleep -Seconds 1
    if ((Get-HealthProblems).Count -eq 0) {
        Write-WatchdogLog 'services recovered after restart'
        Write-Heartbeat 'recovered-after-restart' $true $accountProblems
        return
    }
}

$final = @(Get-HealthProblems)
$message = if ($final.Count) { '恢复失败：' + ($final -join '、') } else { '看门狗拉起失败' }
Write-WatchdogLog ('WATCHDOG_FAILURE: {0}' -f $message)
Send-Alert ('watchdog-failure:' + ($final -join ',')) 'WorkBuddy看门狗启动失败' $message
Write-Heartbeat 'failed' $false @($final + $accountProblems)
exit 1

