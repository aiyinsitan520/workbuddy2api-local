# workbuddy2api 看门狗监督器
# 独立检查主看门狗的心跳和计划任务状态；主看门狗不工作时尝试恢复并告警。
$ErrorActionPreference = 'SilentlyContinue'
$root = $PSScriptRoot
if (-not $root) { $root = Split-Path -Parent $MyInvocation.MyCommand.Path }
$dataDir = Join-Path $root 'data'
$logFile = Join-Path $root 'logs\watchdog.log'
$heartbeatFile = Join-Path $dataDir 'watchdog-heartbeat.json'
$alertFile = Join-Path $dataDir 'watchdog-supervisor-alert.json'
$pauseFile = Join-Path $dataDir 'watchdog-pause'
New-Item -ItemType Directory -Force -Path $dataDir,(Split-Path $logFile) | Out-Null

function Log([string]$m) { try { Add-Content -LiteralPath $logFile -Encoding UTF8 -Value ('[{0}] supervisor: {1}' -f (Get-Date -Format 'yyyy-MM-dd HH:mm:ss'),$m) } catch {} }
function HeartbeatAgeMinutes($hb) {
    try {
        $style = [Globalization.DateTimeStyles]::AssumeUniversal -bor [Globalization.DateTimeStyles]::AdjustToUniversal
        $stamp = [DateTime]::Parse([string]$hb.timestamp,[Globalization.CultureInfo]::InvariantCulture,$style)
        return ((Get-Date).ToUniversalTime() - $stamp.ToUniversalTime()).TotalMinutes
    } catch { return 999999 }
}
function Alert([string]$key,[string]$body) {
    $now=Get-Date
    try {
        $old=if(Test-Path $alertFile){Get-Content $alertFile -Raw|ConvertFrom-Json}else{$null}
        if($old -and $old.key -eq $key -and ((New-TimeSpan -Start ([datetime]$old.sent_at) -End $now).TotalHours -lt 6)){return}
        [pscustomobject]@{key=$key;sent_at=$now.ToString('o');body=$body}|ConvertTo-Json -Compress|Set-Content $alertFile -Encoding UTF8
    } catch {}
    Log ('ALERT: ' + $body)
    try {
        $ps=Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
        $safe=[System.Security.SecurityElement]::Escape($body)
        $cmd="[Windows.UI.Notifications.ToastNotificationManager,Windows.UI.Notifications,ContentType=WindowsRuntime]>`$null;[Windows.Data.Xml.Dom.XmlDocument,Windows.Data.Xml.Dom.XmlDocument,ContentType=WindowsRuntime]>`$null;`$x=New-Object Windows.Data.Xml.Dom.XmlDocument;`$x.LoadXml('<toast><visual><binding template=`"ToastGeneric`"><text>WorkBuddy看门狗监督器</text><text>$safe</text></binding></visual></toast>');[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('WorkBuddy2API').Show((New-Object Windows.UI.Notifications.ToastNotification `$x))"
        Start-Process $ps -ArgumentList '-NoProfile','-NonInteractive','-WindowStyle','Hidden','-Command',$cmd -WindowStyle Hidden|Out-Null
    } catch {}
}

if (Test-Path $pauseFile) {
    $age=(Get-Date)-(Get-Item $pauseFile).LastWriteTime
    if($age.TotalMinutes -lt 5){return}
}
$issues=@()
$task=Get-ScheduledTask -TaskName 'workbuddy2api-watchdog'
if(-not $task){$issues+='主看门狗计划任务不存在'}
elseif($task.State -eq 'Disabled'){$issues+='主看门狗计划任务已禁用'}
if(Test-Path $heartbeatFile) {
    try {
        $hb=Get-Content $heartbeatFile -Raw|ConvertFrom-Json
        $age=HeartbeatAgeMinutes $hb
        if($age -gt 3){$issues+=('主看门狗心跳已中断 {0:N1} 分钟' -f $age)}
    } catch {$issues+='主看门狗心跳文件损坏'}
} else {$issues+='主看门狗尚未写入心跳'}
if($issues.Count -eq 0){return}

$msg=$issues -join '；'
Log $msg
Alert ('supervisor:'+$msg) $msg
try {
    if($task){Start-ScheduledTask -TaskName 'workbuddy2api-watchdog'; Log '已尝试重新启动主看门狗计划任务'}
} catch { Log ('重新启动主看门狗失败: '+$_.Exception.Message) }
Start-Sleep -Seconds 5
if(Test-Path $heartbeatFile) {
    try {
        $hb=Get-Content $heartbeatFile -Raw|ConvertFrom-Json
        $age=HeartbeatAgeMinutes $hb
        if($age -le 3){Log '主看门狗监督恢复成功'; return}
    } catch {}
}
Alert ('supervisor-failed:'+($issues -join ',')) '主看门狗没有恢复，请检查 logs\watchdog.log 和任务计划程序。'
exit 1

