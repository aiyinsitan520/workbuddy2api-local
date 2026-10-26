param([ValidateSet('Start','Stop','Restart','Check','CopyKey','RefreshAccess')][string]$Action='Start',[switch]$NoOpen)
$ErrorActionPreference='Stop'
$root=Split-Path -Parent $PSScriptRoot
$cfgPath=Join-Path $root 'config.json'
if (-not (Test-Path -LiteralPath $cfgPath)) { throw '请将使用入口文件夹保留在原部署目录内。' }
$cfg=Get-Content -LiteralPath $cfgPath -Raw -Encoding UTF8 | ConvertFrom-Json
. (Join-Path $PSScriptRoot 'python-common.ps1')
function Refresh-Access {
    & $entryPython (Join-Path $PSScriptRoot 'generate-access.py')
    if ($LASTEXITCODE -ne 0) {throw '接入信息生成失败。'}
}
function Start-All {
    Refresh-Access
    if ($NoOpen) { & (Join-Path $root 'workbench.ps1') -NoOpen }
    else { & (Join-Path $root 'workbench.ps1') }
    Write-Host '统一后台：http://127.0.0.1:7864/'
    Write-Host 'OpenAI Chat：http://127.0.0.1:7863/v1'
    Write-Host 'Codex Responses：http://127.0.0.1:8787/v1'
    Write-Host 'Claude Code 地址：http://127.0.0.1:8787（不要追加 /v1）'
    Write-Host 'API 密钥：使用入口\API密钥.txt；也可双击复制API密钥.cmd。'
}
function Stop-All {
    & (Join-Path $root 'stop.ps1')
    $scriptPath=Join-Path $root 'workbench\server.py'
    $pidPath=Join-Path $root 'data\workbench.pid'
    $candidates=@()
    if (Test-Path -LiteralPath $pidPath) {
        $savedPid=0
        if ([int]::TryParse((Get-Content -LiteralPath $pidPath -Raw).Trim(),[ref]$savedPid)) {
            $candidates+=@(Get-CimInstance Win32_Process -Filter "ProcessId=$savedPid" -ErrorAction SilentlyContinue)
        }
    }
    foreach ($listener in @(Get-NetTCPConnection -State Listen -LocalPort 7864 -ErrorAction SilentlyContinue)) {
        $candidates+=@(Get-CimInstance Win32_Process -Filter "ProcessId=$($listener.OwningProcess)" -ErrorAction SilentlyContinue)
    }
    foreach ($candidate in $candidates | Where-Object {$_ -and $_.CommandLine -and $_.CommandLine.Contains($scriptPath)} | Sort-Object ProcessId -Unique) {
        Stop-Process -Id $candidate.ProcessId -ErrorAction Stop
    }
    if (Test-Path -LiteralPath $pidPath) {Remove-Item -LiteralPath $pidPath}
    Write-Host '网关、协议兼容层、管理后台均已停止；账号和配置保留。'
}
function Check-All {
    $origin='http://'+$cfg.listen
    $headers=@{Authorization=('Bearer '+$cfg.api_key)}
    $health=Invoke-RestMethod -Uri ($origin+'/healthz') -TimeoutSec 8
    if ($health.service -ne 'workbuddy2api') {throw '网关身份校验失败。'}
    $status=Invoke-RestMethod -Uri ($origin+'/status') -Headers $headers -TimeoutSec 8
    $compat=Invoke-RestMethod -Uri 'http://127.0.0.1:8787/health' -TimeoutSec 8
    if ($compat.source -ne 'unified-account-pool' -or $compat.status -ne 'ok') {throw '协议兼容层未连接统一账号池。'}
    $page=Invoke-WebRequest -Uri 'http://127.0.0.1:7864/' -UseBasicParsing -TimeoutSec 8
    if ($page.StatusCode -ne 200 -or $page.Content -notmatch 'workbench-token') {throw '管理后台校验失败。'}
    $cn=@($status.accounts | Where-Object {$_.realm -eq 'cn'}).Count
    $globalCount=@($status.accounts | Where-Object {$_.realm -eq 'global'}).Count
    $models=Invoke-RestMethod -Uri ($origin+'/v1/models') -Headers $headers -TimeoutSec 8
    Write-Host "检查通过：账号 $($health.total)，可用 $($health.healthy)，国内 $cn，国际 $globalCount。"
    Write-Host "管理后台与兼容接口正常；模型列表 $(@($models.data).Count) 项。"
    Write-Host '本检查不发送模型对话，不消耗对话积分。'
}
try {
    switch ($Action) {
        'Start' {Start-All;Check-All}
        'Stop' {Stop-All}
        'Restart' {Stop-All;Start-All;Check-All}
        'Check' {Check-All}
        'CopyKey' {Set-Clipboard -Value $cfg.api_key;Write-Host '当前 API 密钥已复制到剪贴板。'}
        'RefreshAccess' {Refresh-Access}
    }
} catch {
    $safe=$_.Exception.Message.Replace([string]$cfg.api_key,'[API Key 已隐藏]')
    Write-Host ('操作失败：'+$safe) -ForegroundColor Red
    Write-Host ('日志目录：'+(Join-Path $root 'logs'))
    exit 1
}
