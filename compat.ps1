param([switch]$Stop)
$ErrorActionPreference = 'Stop'
$compatScript = Join-Path $PSScriptRoot 'compat\server.py'
$compatPidFile = Join-Path $PSScriptRoot 'data\compat.pid'

# 兼容层(8787)需要 fastapi/uvicorn/httpx/cryptography。
# 优先用 install.ps1 在 runtime\venv 建好的虚拟环境，避免污染系统 Python。
function Get-CompatPython {
    if ($env:WB2A_COMPAT_PYTHON -and (Test-Path -LiteralPath $env:WB2A_COMPAT_PYTHON)) {
        return $env:WB2A_COMPAT_PYTHON
    }
    $venv = Join-Path $PSScriptRoot 'runtime\venv\Scripts\python.exe'
    if (Test-Path -LiteralPath $venv) { return $venv }
    # 未建虚拟环境：回落到 common.ps1 探测到的解释器（依赖可能缺失，启动会报错并提示）
    if ($env:WB2A_PYTHON) { return $env:WB2A_PYTHON }
    return $null
}
$compatPython = Get-CompatPython
$compatProcess = $null
if (Test-Path -LiteralPath $compatPidFile) {
    $compatSavedPid = 0
    if ([int]::TryParse((Get-Content -LiteralPath $compatPidFile -Raw).Trim(), [ref]$compatSavedPid)) {
        $candidate = Get-CimInstance Win32_Process -Filter "ProcessId=$compatSavedPid" -ErrorAction SilentlyContinue
        if ($candidate -and $candidate.CommandLine.Contains($compatScript)) { $compatProcess = Get-Process -Id $compatSavedPid -ErrorAction SilentlyContinue }
    }
}
if ($Stop) {
    $listeners = @(Get-NetTCPConnection -State Listen -LocalPort 8787 -ErrorAction SilentlyContinue)
    foreach ($listener in $listeners) {
        $candidate = Get-CimInstance Win32_Process -Filter "ProcessId=$($listener.OwningProcess)" -ErrorAction SilentlyContinue
        if ($candidate -and $candidate.CommandLine.Contains($compatScript)) { Stop-Process -Id $candidate.ProcessId }
    }
    if ($compatProcess -and -not $compatProcess.HasExited) { Stop-Process -Id $compatProcess.Id -ErrorAction SilentlyContinue }
    Remove-Item -LiteralPath $compatPidFile -Force -ErrorAction SilentlyContinue
    return
}
$listener = Get-NetTCPConnection -State Listen -LocalPort 8787 -ErrorAction SilentlyContinue | Select-Object -First 1
if ($listener) {
    $candidate = Get-CimInstance Win32_Process -Filter "ProcessId=$($listener.OwningProcess)" -ErrorAction SilentlyContinue
    if (-not $candidate -or -not $candidate.CommandLine.Contains($compatScript)) { throw '8787 端口被其他服务占用。' }
} elseif (-not $compatProcess) {
    if (-not $compatPython -or -not (Test-Path -LiteralPath $compatPython)) {
        throw '兼容层缺少 Python 环境。请先运行 install.ps1 创建 runtime\venv。'
    }
    $env:PYTHONUTF8 = '1'
    $env:PYTHONIOENCODING = 'utf-8'
    $compatProcess = Start-Process -FilePath $compatPython -ArgumentList ('"' + $compatScript + '"') -WorkingDirectory $PSScriptRoot -WindowStyle Hidden -RedirectStandardOutput (Join-Path $PSScriptRoot 'logs\compat.stdout.log') -RedirectStandardError (Join-Path $PSScriptRoot 'logs\compat.stderr.log') -PassThru
    [IO.File]::WriteAllText($compatPidFile, [string]$compatProcess.Id)
}
for ($attempt=0; $attempt -lt 30; $attempt++) {
    try { $health = Invoke-RestMethod -Uri 'http://127.0.0.1:8787/health' -TimeoutSec 2; if ($health.source -eq 'unified-account-pool') { return } } catch { Start-Sleep -Milliseconds 300 }
}
throw '兼容接口启动失败，请查看 logs\compat.stderr.log。'
