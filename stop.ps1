. (Join-Path $PSScriptRoot 'common.ps1')
# 先暂停看门狗，否则它会在停止后一分钟内把服务重新拉起来
New-Item -ItemType File -Path (Join-Path $script:DeploymentRoot 'data\watchdog-pause') -Force | Out-Null
$serverProcess = Get-ManagedServer
if ($serverProcess) {
    $graceful = $false
    try {
        Invoke-RestMethod -Uri "$script:BaseUrl/_manage/shutdown" -Method Post -Headers @{Authorization=('Bearer '+$script:Config.api_key)} -ContentType 'application/json' -Body '{}' -TimeoutSec 5 | Out-Null
        $graceful = $true
    } catch {
        if ($_.Exception.Response -and [int]$_.Exception.Response.StatusCode -eq 409) { throw '任务正在运行，请等待完成后再停止服务。' }
    }
    if (-not $graceful) { Stop-Process -Id $serverProcess.Id }
    if (-not $serverProcess.WaitForExit(10000)) { throw '服务未完成停止，请稍后重试。' }
    Write-Host '服务已停止。'
} else { Write-Host '服务当前未运行。' }
if (Test-Path -LiteralPath $script:PidFile) { Remove-Item -LiteralPath $script:PidFile }

& (Join-Path $PSScriptRoot 'compat.ps1') -Stop
