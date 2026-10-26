$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath $PSScriptRoot
$env:GOCACHE = Join-Path $PSScriptRoot 'runtime\build-cache'
$env:GOMODCACHE = Join-Path $PSScriptRoot 'runtime\module-cache'
$env:GOPATH = Join-Path $PSScriptRoot 'runtime\gopath'
$env:CGO_ENABLED = '0'
if (-not $env:GOPROXY) { $env:GOPROXY = 'https://goproxy.cn,https://proxy.golang.org,direct' }
$goExe = Join-Path $PSScriptRoot 'runtime\go\bin\go.exe'
if (-not (Test-Path -LiteralPath $goExe)) { throw '便携 Go 环境不存在。' }
foreach ($binary in @(@{Name='wb2api.exe';Package='./cmd/server'}, @{Name='login.exe';Package='./cmd/login'}, @{Name='trial.exe';Package='./cmd/trial'})) {
    & $goExe build -trimpath '-ldflags=-s -w' -o $binary.Name $binary.Package
    if ($LASTEXITCODE -ne 0) { throw "编译 $($binary.Name) 失败。" }
}
Write-Host 'Windows 程序编译完成。'
