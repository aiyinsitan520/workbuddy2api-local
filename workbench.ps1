param([switch]$NoOpen)
. (Join-Path $PSScriptRoot 'common.ps1')
& (Join-Path $PSScriptRoot 'start.ps1') -NoLogin
$dashboardPidFile = Join-Path $PSScriptRoot 'data\workbench.pid'
$pythonExe = $env:WB2A_PYTHON
$dashboardScript = Join-Path $PSScriptRoot 'workbench\server.py'
$dashboardProcess = $null
if (Test-Path -LiteralPath $dashboardPidFile) {
    $dashboardPid = 0
    if ([int]::TryParse((Get-Content -LiteralPath $dashboardPidFile -Raw).Trim(), [ref]$dashboardPid)) {
        $candidate = Get-CimInstance Win32_Process -Filter "ProcessId=$dashboardPid" -ErrorAction SilentlyContinue
        if ($candidate -and $candidate.CommandLine.Contains($dashboardScript)) { $dashboardProcess = Get-Process -Id $dashboardPid -ErrorAction SilentlyContinue }
    }
}
if (-not $dashboardProcess) {
    if (Get-NetTCPConnection -State Listen -LocalPort 7864 -ErrorAction SilentlyContinue) { throw '工作台端口 7864 已被其他进程占用。' }
    $dashboardProcess = Start-Process -FilePath $pythonExe -ArgumentList ('"' + $dashboardScript + '"') -WorkingDirectory $PSScriptRoot -WindowStyle Hidden -RedirectStandardOutput (Join-Path $PSScriptRoot 'logs\workbench.stdout.log') -RedirectStandardError (Join-Path $PSScriptRoot 'logs\workbench.stderr.log') -PassThru
    [IO.File]::WriteAllText($dashboardPidFile, [string]$dashboardProcess.Id)
}
for ($attempt=0; $attempt -lt 20; $attempt++) {
    try { $response = Invoke-WebRequest -Uri 'http://127.0.0.1:7864/' -UseBasicParsing -TimeoutSec 2; if ($response.StatusCode -eq 200) { break } } catch { Start-Sleep -Milliseconds 300 }
}
if (-not $response -or $response.StatusCode -ne 200) { throw '工作台启动失败，请查看 logs\workbench.stderr.log。' }
Write-Host '本地工作台已启动：http://127.0.0.1:7864'
if (-not $NoOpen) { Start-Process -FilePath 'http://127.0.0.1:7864' }
