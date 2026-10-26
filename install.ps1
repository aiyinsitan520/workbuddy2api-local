# WorkBuddy2API 本地版 — 一键安装（Windows）
#
# 用法：
#   powershell -NoProfile -ExecutionPolicy Bypass -File install.ps1
#   或直接双击 deploy\一键部署.cmd
#
# 这个脚本只做本机部署需要的事：
#   1. 确认/下载便携 Go 到 runtime\go
#   2. 确认 Python 3，并建 runtime\venv 装兼容层依赖
#   3. 生成随机 API Key，写 config.json
#   4. 编译 wb2api.exe / login.exe / trial.exe
#   5. 装看门狗计划任务（可选，-NoWatchdog 跳过）
# 不修改系统级 Go / Python 环境，不写注册表，不申请管理员权限。

param(
    [switch]$NoWatchdog,   # 跳过看门狗计划任务
    [switch]$SkipBuild     # 只准备环境与配置，不编译
)

$ErrorActionPreference = 'Stop'
$root = $PSScriptRoot
Set-Location -LiteralPath $root

$GO_VERSION = '1.23.4'
$GO_ZIP = "go$GO_VERSION.windows-amd64.zip"
$GO_URL = "https://go.dev/dl/$GO_ZIP"
$GO_PROXY = 'https://goproxy.cn,https://proxy.golang.org,direct'

function Write-Step([string]$text) { Write-Host "`n==> $text" -ForegroundColor Cyan }
function Write-Ok([string]$text) { Write-Host "    [OK] $text" -ForegroundColor Green }
function Write-Warn([string]$text) { Write-Host "    [!] $text" -ForegroundColor Yellow }

# ---------------------------------------------------------------- 目录准备
Write-Step '准备目录'
foreach ($dir in @('auths', 'data', 'logs', 'output', 'runtime', 'backups')) {
    $path = Join-Path $root $dir
    if (-not (Test-Path -LiteralPath $path)) { New-Item -ItemType Directory -Path $path -Force | Out-Null }
}
Write-Ok 'auths / data / logs / output / runtime 就绪'

# ---------------------------------------------------------------- Go
function Get-GoExe {
    $local = Join-Path $root 'runtime\go\bin\go.exe'
    if (Test-Path -LiteralPath $local) { return $local }
    $cmd = Get-Command 'go.exe' -ErrorAction SilentlyContinue
    if ($cmd -and $cmd.Source) { return $cmd.Source }
    return $null
}

Write-Step "检查 Go 环境"
$goExe = Get-GoExe
if ($goExe) {
    Write-Ok "已找到 $goExe"
} else {
    Write-Warn "未找到 Go，开始下载便携版 Go $GO_VERSION"
    $zipPath = Join-Path $root "runtime\$GO_ZIP"
    $target = Join-Path $root 'runtime\go'
    if (Test-Path -LiteralPath $target) { Remove-Item -LiteralPath $target -Recurse -Force }
    # 官方 go.dev 在国内常被墙，按顺序尝试镜像
    $mirrors = @($GO_URL, "https://golang.google.cn/dl/$GO_ZIP", "https://mirrors.aliyun.com/golang/$GO_ZIP")
    $downloaded = $false
    foreach ($url in $mirrors) {
        try {
            Write-Host "    尝试 $url"
            Invoke-WebRequest -Uri $url -OutFile $zipPath -UseBasicParsing -TimeoutSec 600
            if ((Get-Item -LiteralPath $zipPath).Length -gt 10MB) { $downloaded = $true; break }
        } catch {
            Write-Warn "失败：$($_.Exception.Message)"
        }
    }
    if (-not $downloaded) { throw "Go 下载失败。请手动下载 $GO_ZIP 解压到 runtime\go 后重试，或先安装 Go 1.22+。" }
    Write-Host '    解压中…'
    Expand-Archive -LiteralPath $zipPath -DestinationPath (Join-Path $root 'runtime') -Force
    Remove-Item -LiteralPath $zipPath -Force
    $goExe = Join-Path $root 'runtime\go\bin\go.exe'
    if (-not (Test-Path -LiteralPath $goExe)) { throw 'Go 解压后找不到 runtime\go\bin\go.exe。' }
    Write-Ok "便携 Go 就绪：$goExe"
}

# ---------------------------------------------------------------- Python
Write-Step '检查 Python 3'
$pyExe = $null
$pyCandidates = @()
if ($env:WB2A_PYTHON) { $pyCandidates += $env:WB2A_PYTHON }
$portablePy = Join-Path $root 'runtime\python\python.exe'
if (Test-Path -LiteralPath $portablePy) { $pyCandidates += $portablePy }
$pyLauncher = Get-Command 'py.exe' -ErrorAction SilentlyContinue
if ($pyLauncher) {
    try {
        $resolved = & $pyLauncher.Source -3 -c "import sys; print(sys.executable)" 2>$null
        if ($LASTEXITCODE -eq 0 -and $resolved) { $pyCandidates += $resolved.Trim() }
    } catch { }
}
foreach ($name in @('python.exe', 'python3.exe')) {
    $cmd = Get-Command $name -ErrorAction SilentlyContinue
    if ($cmd -and $cmd.Source) { $pyCandidates += $cmd.Source }
}
foreach ($base in @($env:LOCALAPPDATA, $env:ProgramFiles, 'C:\')) {
    if (-not $base) { continue }
    foreach ($ver in @('315', '314', '313', '312', '311', '310', '39')) {
        $pyCandidates += (Join-Path $base "Programs\Python\Python$ver\python.exe")
        $pyCandidates += (Join-Path $base "Python$ver\python.exe")
    }
}
foreach ($candidate in $pyCandidates) {
    if (-not $candidate -or -not (Test-Path -LiteralPath $candidate)) { continue }
    if ($candidate -like '*\WindowsApps\*') { continue }
    try {
        $ver = & $candidate -c "import sys; print('%d.%d' % sys.version_info[:2])" 2>$null
        if ($LASTEXITCODE -eq 0 -and $ver -match '^\d+\.\d+$') {
            $pyExe = (Resolve-Path -LiteralPath $candidate).Path
            break
        }
    } catch { }
}
if (-not $pyExe) {
    throw @'
未找到 Python 3。请任选其一后重新运行本脚本：
  1. 安装 Python 3.10+：https://www.python.org/downloads/windows/（安装时勾选 Add to PATH）
  2. 下载 Python embeddable 包解压到 runtime\python\（目录内需有 python.exe）
安装完成后重跑 install.ps1 即可，已完成的步骤会跳过。
'@
}
Write-Ok "Python：$pyExe"
$env:WB2A_PYTHON = $pyExe

# ---------------------------------------------------------------- 兼容层 venv
Write-Step '准备兼容层虚拟环境（8787 需要 fastapi / uvicorn / httpx / cryptography）'
$venvDir = Join-Path $root 'runtime\venv'
$venvPy = Join-Path $venvDir 'Scripts\python.exe'
if (-not (Test-Path -LiteralPath $venvPy)) {
    Write-Host '    创建 runtime\venv …'
    & $pyExe -m venv $venvDir
    if ($LASTEXITCODE -ne 0) { throw '创建虚拟环境失败。' }
}
Write-Host '    安装依赖 …'
# PyPI 直连在国内常被 SSL 中断，按顺序尝试镜像；任一源成功即停。
$pipMirrors = @(
    'https://pypi.tuna.tsinghua.edu.cn/simple',
    'https://mirrors.aliyun.com/pypi/simple',
    'https://mirrors.ustc.edu.cn/pypi/simple'
) + @('')
$pipInstalled = $false
foreach ($index in $pipMirrors) {
    $pipArgs = @('-m', 'pip', 'install', '--quiet', '--disable-pip-version-check',
        '--timeout', '30', '--retries', '1',
        'fastapi>=0.110', 'uvicorn>=0.27', 'httpx>=0.27', 'cryptography>=42')
    if ($index) {
        $hostName = ([System.Uri]$index).Host
        $pipArgs += @('-i', $index, '--trusted-host', $hostName)
    }
    if ($index) { Write-Host "    尝试源 $index" } else { Write-Host '    尝试默认 PyPI' }
    & $venvPy @pipArgs
    if ($LASTEXITCODE -eq 0) { $pipInstalled = $true; break }
    Write-Warn '该源失败，换下一个'
}
if (-not $pipInstalled) {
    throw @'
兼容层依赖安装失败。可任选其一后重跑：
  1. 配置代理：$env:HTTPS_PROXY = 'http://127.0.0.1:端口' 后重跑 install.ps1
  2. 手动安装：runtime\venv\Scripts\python.exe -m pip install -i https://pypi.tuna.tsinghua.edu.cn/simple fastapi uvicorn httpx cryptography
'@
}
# pip 自升级失败不影响依赖使用，仅提示。
& $venvPy -m pip install --upgrade pip --quiet --disable-pip-version-check -i https://pypi.tuna.tsinghua.edu.cn/simple 2>$null
Write-Ok 'runtime\venv 就绪'

# ---------------------------------------------------------------- config.json
Write-Step '生成网关配置'
$configPath = Join-Path $root 'config.json'
if (Test-Path -LiteralPath $configPath) {
    Write-Warn 'config.json 已存在，保留现有配置（不覆盖你的密钥与账号设置）'
} else {
    $cfg = Get-Content -LiteralPath (Join-Path $root 'config.example.json') -Raw -Encoding UTF8 | ConvertFrom-Json
    # 只监听本机回环，避免把账号池暴露到局域网
    $cfg.listen = '127.0.0.1:7863'
    $bytes = New-Object byte[] 32
    [System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
    $cfg.api_key = 'wb-' + ([System.BitConverter]::ToString($bytes) -replace '-', '').ToLower()
    $cfg.upstream.client_name = 'WorkBuddy'
    $cfg | ConvertTo-Json -Depth 12 | Set-Content -LiteralPath $configPath -Encoding UTF8
    Write-Ok 'config.json 已生成（含随机 API Key）'
}

# 兼容层的旧密钥表：空数组表示不额外放行旧密钥
$compatCfg = Join-Path $root 'compat\config.json'
if (-not (Test-Path -LiteralPath $compatCfg)) {
    '{"legacy_api_keys": []}' | Set-Content -LiteralPath $compatCfg -Encoding UTF8
}

# ---------------------------------------------------------------- 编译
if (-not $SkipBuild) {
    Write-Step '编译 Windows 程序'
    $env:GOCACHE = Join-Path $root 'runtime\build-cache'
    $env:GOMODCACHE = Join-Path $root 'runtime\module-cache'
    $env:GOPATH = Join-Path $root 'runtime\gopath'
    $env:CGO_ENABLED = '0'
    if (-not $env:GOPROXY) { $env:GOPROXY = $GO_PROXY }
    Write-Host '    拉取模块依赖 …'
    & $goExe -C $root mod download
    if ($LASTEXITCODE -ne 0) { throw 'go mod download 失败。可设置 GOPROXY 后重试。' }
    foreach ($item in @(
            @{ Name = 'wb2api.exe'; Package = './cmd/server' },
            @{ Name = 'login.exe'; Package = './cmd/login' },
            @{ Name = 'trial.exe'; Package = './cmd/trial' })) {
        Write-Host "    构建 $($item.Name) …"
        & $goExe -C $root build -trimpath '-ldflags=-s -w' -o (Join-Path $root $item.Name) $item.Package
        if ($LASTEXITCODE -ne 0) { throw "编译 $($item.Name) 失败。" }
    }
    Write-Ok 'wb2api.exe / login.exe / trial.exe 构建完成'
} else {
    Write-Warn '已跳过编译（-SkipBuild）。启动前需自行准备 wb2api.exe。'
}

# ---------------------------------------------------------------- 入口信息
Write-Step '生成接入信息'
& $venvPy (Join-Path $root '使用入口\generate-access.py')
if ($LASTEXITCODE -ne 0) { Write-Warn '接入信息生成失败，可稍后双击「刷新接入信息.cmd」重试。' }
else { Write-Ok '已写入 使用入口\API密钥.txt 与 connection.txt' }

# ---------------------------------------------------------------- 看门狗
if (-not $NoWatchdog) {
    Write-Step '安装看门狗计划任务'
    try {
        & (Join-Path $root 'watchdog-install.ps1')
    } catch {
        Write-Warn "看门狗安装失败（不影响手动启动）：$($_.Exception.Message)"
    }
} else {
    Write-Warn '已跳过看门狗安装（-NoWatchdog）'
}

# ---------------------------------------------------------------- 完成
$finalCfg = Get-Content -LiteralPath $configPath -Raw -Encoding UTF8 | ConvertFrom-Json
Write-Host ''
Write-Host '============================================================' -ForegroundColor Green
Write-Host ' 安装完成' -ForegroundColor Green
Write-Host '============================================================' -ForegroundColor Green
Write-Host ''
Write-Host '  下一步：'
Write-Host '    1. 双击  使用入口\启动服务.cmd   启动网关 + 工作台'
Write-Host '    2. 浏览器打开  http://127.0.0.1:7864/  添加账号'
Write-Host ''
Write-Host '  API 地址：'
Write-Host "    OpenAI Chat     http://$($finalCfg.listen)/v1"
Write-Host '    Codex Responses http://127.0.0.1:8787/v1'
Write-Host '    Claude Code     http://127.0.0.1:8787   （不要加 /v1）'
Write-Host ''
Write-Host "  API Key：$($finalCfg.api_key)"
Write-Host '  （也可从 使用入口\API密钥.txt 或 connection.txt 读取）'
Write-Host ''
Write-Host '  注意：auths\ 下保存账号凭据，不要分享该目录或 config.json。' -ForegroundColor Yellow
Write-Host ''
