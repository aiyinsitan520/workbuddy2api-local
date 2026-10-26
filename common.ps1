$ErrorActionPreference = 'Stop'
$script:DeploymentRoot = $PSScriptRoot
Set-Location -LiteralPath $script:DeploymentRoot
$script:Config = Get-Content -LiteralPath (Join-Path $script:DeploymentRoot 'config.json') -Raw -Encoding UTF8 | ConvertFrom-Json
$script:BaseUrl = 'http://' + $script:Config.listen
$script:PidFile = Join-Path $script:DeploymentRoot 'data\server.pid'
$script:ServerExe = Join-Path $script:DeploymentRoot 'wb2api.exe'
$env:TEMP = Join-Path $script:DeploymentRoot 'data'
$env:TMP = $env:TEMP
$env:PYTHONUTF8 = '1'
$env:PYTHONIOENCODING = 'utf-8'

# Python 解释器探测：工作台(7864)与兼容层(8787)都依赖它。
# 顺序：显式环境变量 → 部署内便携 Python → py 启动器 → PATH → 常见安装位置。
# 注意 py.exe 是启动器不是解释器，找不到真解释器时会拉起商店别名，故只取其返回值。
function Resolve-Wb2aPython {
    $candidates = @()
    if ($env:WB2A_PYTHON) { $candidates += $env:WB2A_PYTHON }
    $candidates += (Join-Path $script:DeploymentRoot 'runtime\python\python.exe')
    $pyLauncher = Get-Command 'py.exe' -ErrorAction SilentlyContinue
    if ($pyLauncher) {
        try {
            $resolved = & $pyLauncher.Source -3 -c "import sys; print(sys.executable)" 2>$null
            if ($LASTEXITCODE -eq 0 -and $resolved) { $candidates += $resolved.Trim() }
        } catch { }
    }
    foreach ($name in @('python.exe','python3.exe')) {
        $cmd = Get-Command $name -ErrorAction SilentlyContinue
        if ($cmd -and $cmd.Source) { $candidates += $cmd.Source }
    }
    foreach ($base in @($env:LOCALAPPDATA, $env:ProgramFiles, $env:ProgramFiles + ' (x86)', 'C:\')) {
        if (-not $base) { continue }
        foreach ($ver in @('313','312','311','310','39')) {
            $candidates += (Join-Path $base "Programs\Python\Python$ver\python.exe")
            $candidates += (Join-Path $base "Python$ver\python.exe")
        }
    }
    foreach ($candidate in $candidates) {
        if (-not $candidate -or -not (Test-Path -LiteralPath $candidate)) { continue }
        # 排除 Windows 商店占位符（0 字节的 WindowsApps 别名）
        if ($candidate -like '*\WindowsApps\*') { continue }
        try {
            $version = & $candidate -c "import sys; print('%d.%d' % sys.version_info[:2])" 2>$null
            if ($LASTEXITCODE -eq 0 -and $version -match '^\d+\.\d+$') {
                return (Resolve-Path -LiteralPath $candidate).Path
            }
        } catch { }
    }
    return $null
}

$env:WB2A_PYTHON = Resolve-Wb2aPython
if (-not $env:WB2A_PYTHON) {
    throw '未找到 Python 3。请运行 install.ps1（会自动安装），或手动安装 Python 3.10+ 后重试。'
}
function Get-ManagedServer {
    if (-not (Test-Path -LiteralPath $script:PidFile)) { return $null }
    $serverPid = 0
    if (-not [int]::TryParse((Get-Content -LiteralPath $script:PidFile -Raw).Trim(), [ref]$serverPid)) { return $null }
    $serverProcess = Get-Process -Id $serverPid -ErrorAction SilentlyContinue
    if ($serverProcess -and $serverProcess.Path -eq $script:ServerExe) { return $serverProcess }
    return $null
}
function Get-GatewayHealth {
    $healthJson = & (Join-Path $env:SystemRoot 'System32\curl.exe') --silent --max-time 3 "$script:BaseUrl/healthz"
    if ($LASTEXITCODE -eq 0 -and $healthJson) {
        try { return ($healthJson | ConvertFrom-Json) } catch { return $null }
    }
    return $null
}
