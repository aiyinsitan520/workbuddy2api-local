param(
    [ValidateSet('global','cn')][string]$Realm = 'global',
    [switch]$RequestOnly,
    [switch]$PollOnly,
    [int]$TimeoutSeconds = 600
)
. (Join-Path $PSScriptRoot 'common.ps1')
$loginExe = Join-Path $script:DeploymentRoot 'login.exe'
$stateFile = Join-Path $env:TEMP 'wb2api-login-state.json'
$pollErrorFile = Join-Path $script:DeploymentRoot 'logs\login-poll.stderr.log'
$label = if ($Realm -eq 'cn') { '国内 CodeBuddy' } else { '国际 WorkBuddy' }
if (-not $PollOnly) {
    $authUrl = & $loginExe "--realm=$Realm" url
    if ($LASTEXITCODE -ne 0) { throw "无法取得 $Realm 版登录链接。" }
    $authUrl = ($authUrl -join '').Trim()
    $parsedUrl = [Uri]$authUrl
    $hostOkay = if ($Realm -eq 'cn') {
        $parsedUrl.Host -eq 'copilot.tencent.com'
    } else {
        $parsedUrl.Host -eq 'workbuddy.ai' -or $parsedUrl.Host.EndsWith('.workbuddy.ai')
    }
    if ($parsedUrl.Scheme -ne 'https' -or -not $hostOkay) { throw '上游返回了意外的登录域名，请检查。' }
    Write-Host "请在浏览器完成 $label 登录：$authUrl"
    if ($RequestOnly) { return }
    Start-Process -FilePath $authUrl
}
if (-not (Test-Path -LiteralPath $stateFile)) { throw '未找到登录状态，请重新运行 login.cmd。' }
Write-Host '正在等待浏览器授权，最多等待 10 分钟……'
$deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
$tokenResult = $null
while ([DateTime]::UtcNow -lt $deadline) {
    $ErrorActionPreference = 'Continue'
    try { $pollJson = & $loginExe "--realm=$Realm" poll 2>$pollErrorFile } finally { $ErrorActionPreference = 'Stop' }
    if ($LASTEXITCODE -eq 0 -and $pollJson) {
        $tokenResult = ($pollJson -join '') | ConvertFrom-Json
        break
    }
    Start-Sleep -Seconds 3
}
if (-not $tokenResult) { throw '登录等待超时，请重新运行 login.cmd。' }
if (-not $tokenResult.access_token -or -not $tokenResult.refresh_token -or -not $tokenResult.uid) { throw '上游没有返回完整账号信息，请重新登录。' }
$safeUid = [string]$tokenResult.uid
if ($safeUid -notmatch '^[A-Za-z0-9_-]+$') { throw '上游账号 ID 格式不符合预期。' }
$authDocument = @{
    account = @{ uid = $safeUid; enterpriseId = $tokenResult.enterprise_id; nickname = $tokenResult.nickname }
    auth = @{ accessToken = $tokenResult.access_token; refreshToken = $tokenResult.refresh_token; expiresAt = ([DateTimeOffset]::UtcNow.ToUnixTimeSeconds() + [long]$tokenResult.expires_in); domain = $tokenResult.domain; realm = $Realm }
}
$authFile = Join-Path $script:DeploymentRoot "auths\workbuddy-$safeUid.json"
[IO.File]::WriteAllText($authFile, ($authDocument | ConvertTo-Json -Depth 6), [Text.UTF8Encoding]::new($false))
$tokenResult = $null
$pollJson = $null
$authDocument = $null
Write-Host "$label 账号已保存，正在重启服务加载账号。"
& (Join-Path $PSScriptRoot 'stop.ps1')
& (Join-Path $PSScriptRoot 'start.ps1') -NoLogin
if ($Realm -eq 'cn') {
    Write-Host '登录完成。模型名称请使用 cn: 前缀，例如 cn:deepseek-v4.1-flash。'
} else {
    Write-Host '登录完成。模型名称请使用 global: 前缀，例如 global:default-model。'
}
