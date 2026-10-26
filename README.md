<p align="center">
  <img src="https://raw.githubusercontent.com/DGZSbot/ai-icon/refs/heads/main/WorkBuddy.png" alt="WorkBuddy2API" width="110">
</p>

<h1 align="center">WorkBuddy2API · 本地版</h1>

<p align="center">
  <b>把 WorkBuddy / CodeBuddy 账号变成 OpenAI 兼容 API</b><br>
  多账号池 · 三种协议入口 · 浏览器工作台 · Windows 一键部署
</p>

<p align="center">
  <img alt="Go" src="https://img.shields.io/badge/Go-1.22%2B-00ADD8?logo=go&logoColor=white&style=flat-square">
  <img alt="Python" src="https://img.shields.io/badge/Python-3.10%2B-3776AB?logo=python&logoColor=white&style=flat-square">
  <img alt="Platform" src="https://img.shields.io/badge/Windows-10%20%2F%2011-0078D4?logo=windows&logoColor=white&style=flat-square">
  <img alt="License" src="https://img.shields.io/badge/License-MIT-green?style=flat-square">
</p>

---

## 这是什么

一个跑在你自己电脑上的**反向代理网关**。它把 WorkBuddy（国际版）和 CodeBuddy（国内版）的账号登录态，转换成标准的 OpenAI / Anthropic 接口，让现有的客户端（Codex、Claude Code、Cherry Studio、任意 OpenAI SDK）零改造接入。

上游官方并不提供 OpenAI 形态的开放 API。本项目通过官方 OAuth 设备授权拿到账号凭据，在本地做 token 自动刷新、账号池调度和流量治理，只对本机暴露接口。

> ⚠️ **使用边界**：本项目是非官方网关。请只用于**你自己的、已授权到你名下的账号**，仅在本机或私有环境运行。不要用它批量注册账号、伪造活跃、规避上游风控，或对外提供公共代理服务。上游的模型可用性、签到与奖励规则随时可能变化，且由上游单方面决定。

## 三个入口，一个账号池

```
                         ┌──────────────────────────────────┐
   OpenAI SDK ──────────▶│  :7863  Go 网关                  │
   Cherry Studio          │  /v1/chat/completions            │
                         │  /v1/models                      │
                         └───────────────┬──────────────────┘
                                         │
   Codex ───────────────▶┌───────────────▼──────────────────┐
   (Responses)           │  :8787  协议兼容层 (Python)      │
                         │  /v1/responses                   │
   Claude Code ─────────▶│  /v1/messages                    │
   (Anthropic Messages)  └───────────────┬──────────────────┘
                                         │
                         ┌───────────────▼──────────────────┐
                         │  账号池（CN + Global 混合）      │
                         │  加权选号 · 冷却 · 熔断 · 粘性   │
                         └───────────────┬──────────────────┘
                                         │
                         ┌───────────────▼──────────────────┐
                         │  WorkBuddy / CodeBuddy 上游      │
                         └──────────────────────────────────┘

   :7864  浏览器工作台 ── 账号管理 / 用量统计 / 定时任务 / 连接测试 / 日志
```

| 端口 | 组件 | 语言 | 作用 |
| --- | --- | --- | --- |
| 7863 | `wb2api.exe` | Go | 主网关。OpenAI 兼容 Chat、账号池、调度器 |
| 8787 | `compat/server.py` | Python | 协议兼容层。Responses / Anthropic Messages，复用同一个池 |
| 7864 | `workbench/server.py` | Python | 本地工作台，只监听回环地址 |

模型前缀决定路由：`cn:xxx` 走国内账号，`global:xxx` 走国际账号。裸模型名默认国内。**模型名必须显式填写**，网关不会替你选默认值。

## 一键部署

### 前置条件

- Windows 10 / 11
- 任意盘符下**至少 3 GB 空闲空间**（Go 工具链 + 构建缓存）
- 首次安装需要联网（下载 Go、Python 依赖）

Go 和 Python 都不必预装：安装脚本会下载便携 Go，并在 `runtime\venv` 里装兼容层依赖。如果你已经有 Python 3.10+，脚本会直接复用。

### 安装

```powershell
git clone https://github.com/aiyinsitan520/workbuddy2api-local.git
cd workbuddy2api-local
.\一键部署.cmd
```

或者下载 ZIP 解压后双击 `一键部署.cmd`。

脚本会依次完成：准备目录 → 下载/复用 Go → 探测/复用 Python → 建 `runtime\venv` 装依赖 → 生成随机 API Key 写 `config.json` → 编译三个 exe → 生成接入信息 → 注册看门狗计划任务。全过程**不需要管理员权限**，也不改系统环境变量。

可选参数：

```powershell
.\install.ps1 -NoWatchdog   # 不装看门狗计划任务
.\install.ps1 -SkipBuild    # 只准备环境与配置，不编译
```

### 启动与添加账号

```powershell
.\使用入口\启动服务.cmd
```

浏览器打开 `http://127.0.0.1:7864/`，进入「账号管理 → 添加账号」，选国内版或国际版，点「生成登录链接」，在官方页面完成**CLI 授权**（只登录进首页不算），回到工作台等待自动保存。

也可以导入已有的 `.json` / `.info` 凭据文件。

### 接入客户端

装好后地址与密钥写在两处：`connection.txt` 和 `使用入口\API密钥.txt`。

| 客户端 | Base URL | 协议 |
| --- | --- | --- |
| OpenAI SDK / Cherry Studio | `http://127.0.0.1:7863/v1` | Chat Completions |
| Codex | `http://127.0.0.1:8787/v1` | Responses |
| Claude Code | `http://127.0.0.1:8787` | Anthropic Messages（**不加 /v1**） |

```powershell
$cfg = Get-Content '.\config.json' -Raw -Encoding UTF8 | ConvertFrom-Json
$headers = @{ Authorization = 'Bearer ' + $cfg.api_key }
$body = @{ model='cn:deepseek-v4.1-flash'; messages=@(@{role='user';content='你好'}); stream=$false } | ConvertTo-Json -Depth 6
Invoke-RestMethod 'http://127.0.0.1:7863/v1/chat/completions' -Method Post -Headers $headers `
  -ContentType 'application/json' -Body ([Text.Encoding]::UTF8.GetBytes($body)) |
  Select-Object -ExpandProperty choices | Select-Object -ExpandProperty message
```

完整客户端配置见 [`使用入口/配置教程.md`](使用入口/配置教程.md)。

## 工作台

`http://127.0.0.1:7864/` 共七个页面：

| 页面 | 内容 |
| --- | --- |
| 工作台概览 | 可用账号数、CN/Global 积分、已启用任务、成功请求数、近 7 天用量 |
| 账号管理 | 双域分组、搜索筛选、逐账号「参与调用」开关、积分用量、凭据有效期与剩余天数 |
| 用量统计 | 日 / 7 天 / 周 / 月 / 自定义区间，Token、请求数、积分、延迟四指标，按账号/模型/天三视图 |
| 自动任务 | 六类任务的独立开关与整点排程，国际版日常对话任务 |
| 连接测试 | 发一条真实请求验证全链路，可读取实时模型列表 |
| 接入与提示词 | 复制地址与密钥，切换透传/自定义系统提示词，指纹脱敏开关 |
| 运行日志 | 滚动显示网关日志，已知 token 与密钥会被隐藏 |

页面细节与设计取舍见 [`docs/workbench.md`](docs/workbench.md)。

## 核心机制

**账号池**
- 四因子加权选号：积分比例 ×10、闲置补偿、成功率 ×3、快过期积分占比 ×8
- 防惊群：跳过 100ms 内刚被选中的账号
- 在途租约：单账号并发上限 `pool.max_in_flight`，占满即退出候选
- 会话粘性：同一会话 30 分钟内尽量落在同一账号

**容错**
- 429 软冷却（600s 起指数退避，封顶 2h）
- 402 / 余额耗尽 → 硬冷却至次日 04:00
- 连续失败熔断，指数退避封顶 6h
- 模型级限流独立冷却，切其他模型立即可用
- 裸 401 HTML 判定为凭据失效，硬禁用避免死号拖垮轮转

**请求链路**
- 出站强制 `stream:true`，SSE 帧按 OpenAI 规范白名单重建
- 非流式在本地聚合为单响应
- 会话头族注入，轮转/重试复用同一聚合键
- 提示词默认透传，可选自定义替换

**定时任务**（北京时间，六类独立开关）

| 任务 | 默认时间 | 适用 |
| --- | --- | --- |
| 签到 | 09 / 21 | 国内 |
| 猫猫旅行 | 09 / 21 | 国内 |
| 活跃上报 | 10 | 国内 |
| Token 保活 | 22 | 双域 |
| 开学季 | 12 | 国内 |
| 夜猫子 | 01 | 国内 |

凭据到期提醒：refresh token 剩余 ≤25 天时每天弹一次系统通知，启动 45 秒后还会补跑一次被错过的检查。

## 目录结构

```
workbuddy2api-local/
├── 一键部署.cmd          ← 双击这个
├── install.ps1           ← 安装脚本主体
├── start.ps1 / stop.ps1  ← 网关启停
├── build.ps1             ← 重新编译 Go
├── workbench.ps1         ← 工作台启动器
├── compat.ps1            ← 兼容层启动器
├── watchdog*.ps1         ← 看门狗与监督器
├── cmd/                  ← Go 入口（server / login / trial / credit …）
├── internal/             ← 网关实现（pool / scheduler / server / upstream / auth）
├── workbench/            ← 工作台前端与后端
├── compat/               ← 协议转换层
├── scripts/              ← 任务辅助脚本
├── 使用入口/              ← 日常快捷方式、教程、客户端配置示例
├── docs/                 ← 架构与页面说明
├── config.example.json   ← 配置模板
└── Dockerfile            ← 仅网关容器（不含工作台）
```

`auths/`、`data/`、`logs/`、`output/`、`runtime/`、`config.json` 都在 `.gitignore` 里，安装后本地生成。

## 重新编译

改了 Go 代码必须重新编译，改源码不会自动生效：

```powershell
.\build.ps1
.\使用入口\停止服务.cmd
.\使用入口\启动服务.cmd
```

只改 `workbench/app.js` 或 CSS 的话，刷新浏览器即可，不用重启（工作台每次请求都从磁盘重读静态文件）。

## 常见问题

| 现象 | 原因与处理 |
| --- | --- |
| `503 no_healthy_account` | 所有账号都在冷却/暂停/凭据失效/积分不足。看工作台账号页的状态列 |
| `11102 model [...] service info not found` | 模型名写错，或该账号不服务这个模型。`cn:default-model` 国内不存在 |
| `11140 request illegal` | 上游 chat 通道封禁。先重新登录；重登无效则该号只能放弃 |
| `14018 Credits exhausted` | 该账号积分用完，通道本身是通的 |
| `11-128` | 请求没被识别为官方客户端。用网关调用，不要手写裸 POST 探测 |
| 后台打不开 | 服务没启动。跑 `检查服务.cmd` 看三个端口 |
| 兼容层启动失败 | `runtime\venv` 缺依赖，重跑 `install.ps1` |
| 改了代码没变化 | 需要 `build.ps1` 重新编译并重启 |

## 安全须知

- `auths/` 里是账号凭据（明文 JWT），`config.json` 里是网关密钥。**不要分享、不要提交、不要贴到任何地方。**
- `logs/compat.log` 记录完整请求体，属于敏感文件，排查时不要全量 grep 或外传。
- 网关默认只监听 `127.0.0.1`。要改监听地址前，先想清楚会不会把账号池暴露到局域网或公网。
- 工作台没有独立密码：它靠回环地址 + 会话随机令牌 + Host/Origin 校验保护。不要把它反向代理到公网。

## 上游与致谢

本项目基于 [maylag/Sliverkiss-workbuddy2api](https://github.com/maylag/Sliverkiss-workbuddy2api) 的账号池网关，增加了本地工作台、协议兼容层、看门狗与 Windows 一键部署。

- 工作台页面样式参考 [ShouZhuo0413/codebuddy2api](https://github.com/ShouZhuo0413/codebuddy2api)，许可证见 `workbench/LICENSE.reference`
- 协议转换层复用原项目的 `core` 转换代码，许可证见 `compat/LICENSE.reference`
- 原始网关项目：见上游仓库

## 许可

MIT。详见 [LICENSE](LICENSE)。
