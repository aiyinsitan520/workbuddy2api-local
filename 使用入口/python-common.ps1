# 使用入口脚本共用的 Python 探测逻辑。
# 入口脚本可能被双击运行，工作目录不确定，因此一律以脚本自身位置推部署根目录。
$ErrorActionPreference = 'Stop'
$entryRoot = $PSScriptRoot
$deployRoot = Split-Path -Parent $entryRoot

function Resolve-EntryPython {
    if ($env:WB2A_PYTHON -and (Test-Path -LiteralPath $env:WB2A_PYTHON)) { return $env:WB2A_PYTHON }
    $portable = Join-Path $deployRoot 'runtime\python\python.exe'
    if (Test-Path -LiteralPath $portable) { return $portable }
    $venv = Join-Path $deployRoot 'runtime\venv\Scripts\python.exe'
    if (Test-Path -LiteralPath $venv) { return $venv }
    $pyLauncher = Get-Command 'py.exe' -ErrorAction SilentlyContinue
    if ($pyLauncher) {
        try {
            $resolved = & $pyLauncher.Source -3 -c "import sys; print(sys.executable)" 2>$null
            if ($LASTEXITCODE -eq 0 -and $resolved) { return $resolved.Trim() }
        } catch { }
    }
    foreach ($name in @('python.exe','python3.exe')) {
        $cmd = Get-Command $name -ErrorAction SilentlyContinue
        if ($cmd -and $cmd.Source -and $cmd.Source -notlike '*\WindowsApps\*') { return $cmd.Source }
    }
    foreach ($base in @($env:LOCALAPPDATA, $env:ProgramFiles)) {
        if (-not $base) { continue }
        foreach ($ver in @('315','314','313','312','311','310','39')) {
            $candidate = Join-Path $base "Programs\Python\Python$ver\python.exe"
            if (Test-Path -LiteralPath $candidate) { return $candidate }
        }
    }
    return $null
}

$entryPython = Resolve-EntryPython
if (-not $entryPython) { throw '未找到 Python 3，请先运行部署根目录的 一键部署.cmd（或 install.ps1）。' }
$env:WB2A_PYTHON = $entryPython
$env:PYTHONUTF8 = '1'
$env:PYTHONIOENCODING = 'utf-8'
