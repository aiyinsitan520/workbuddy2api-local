"""Regenerate only local handoff files from the authoritative gateway configuration."""
from pathlib import Path
from datetime import datetime
import json
OUT=Path(__file__).resolve().parent;ROOT=OUT.parent
cfg=json.loads((ROOT/'config.json').read_text(encoding='utf-8-sig'))
key=cfg['api_key'];origin='http://'+cfg['listen']
def write(name,text):
 p=OUT/name;p.parent.mkdir(parents=True,exist_ok=True);p.write_text(text,encoding='utf-8-sig')
write('API密钥.txt',key+'\n')
write('admin-access.txt',f'''更新时间：{datetime.now().isoformat(timespec='seconds')}
管理后台：http://127.0.0.1:7864/
后台登录：当前本机后台没有独立用户名或密码，页面会话标记由服务生成。
OpenAI Chat Base URL：{origin}/v1
Codex Responses Base URL：http://127.0.0.1:8787/v1
Claude Code ANTHROPIC_BASE_URL：http://127.0.0.1:8787
API Key：{key}
国内模型：cn:deepseek-v4.1-flash（推荐默认）/ cn:fast-model
国际模型：global:fast-model / global:default-model
新增国际模型：global:gpt-6-sol / global:gpt-6.1-sol / global:gpt-6-luna
API Key 用于以上两个 API 入口，不能填为账号 accessToken。
原国内版 API Key 仅在 8787 兼容入口继续有效。
服务仅限当前 Windows 本机。旧启动程序会尝试启动原部署，请使用本目录的启动服务.cmd。
''')
(ROOT/'connection.txt').write_text(f'Base URL: {origin}/v1\nAPI Key: {key}\nAdmin: http://127.0.0.1:7864/\nCompat Base URL: http://127.0.0.1:8787/v1\n',encoding='utf-8')
# 国内默认模型选 deepseek-v4.1-flash：实测唯一「8/8 全过 + 273 tok/s 最快 + 0.075 credit/轮 最便宜」。
# 不用 cn:fast-model（现路由到 glm-5.3-flash，0.107 credit/轮），也不用 cn:default-model——
# 国内上游没有这个别名，实测返回 11102 model [default-model] service info not found。
# 证据见 data/CN模型能力性价比表.md。
CN_MODEL='cn:deepseek-v4.1-flash'
GLOBAL_MODEL='global:fast-model'
# 配置示例是随仓库分发的模板，必须留占位符：真实密钥只写在 API密钥.txt / admin-access.txt，
# 那两个文件已被 .gitignore 排除。否则一次 install 就会把本机密钥泄漏进示例文件。
PLACEHOLDER='<YOUR-GATEWAY-API-KEY>'
for realm in ('cn','global'):
 model=CN_MODEL if realm=='cn' else GLOBAL_MODEL
 # CN 侧四个槽位全部指向同一个模型：其余候选要么需要显式 reasoning_effort（glm-5.1），
 # 要么贵 2–14 倍，要么在部分账号上不可服务。国际侧保留 default-model（该别名在 workbuddy.ai 可用）。
 claude={'model':model,'env':{'ANTHROPIC_BASE_URL':'http://127.0.0.1:8787','ANTHROPIC_AUTH_TOKEN':PLACEHOLDER,'ANTHROPIC_DEFAULT_SONNET_MODEL':model,'ANTHROPIC_DEFAULT_OPUS_MODEL':model if realm=='cn' else realm+':default-model','ANTHROPIC_DEFAULT_HAIKU_MODEL':model}}
 p=OUT/'配置示例'/('claude-'+realm+'.json');p.parent.mkdir(parents=True,exist_ok=True);p.write_text(json.dumps(claude,ensure_ascii=False,indent=2)+'\n',encoding='utf-8')
 codex=f'''# 仅示例，按教程合并到现有 config.toml，勿覆盖整个配置。
model = "{model}"
model_provider = "workbuddy_{realm}"

[model_providers.workbuddy_{realm}]
name = "WorkBuddy {realm}"
base_url = "http://127.0.0.1:8787/v1"
env_key = "WORKBUDDY_API_KEY"
wire_api = "responses"
requires_openai_auth = false
supports_websockets = false
'''
 (OUT/'配置示例'/('codex-'+realm+'.toml')).write_text(codex,encoding='utf-8')
 write('配置示例/openai-'+realm+'.json',json.dumps({'base_url':origin+'/v1','api_key':PLACEHOLDER,'model':model},ensure_ascii=False,indent=2)+'\n')
codex61='''# 按教程合并到现有 config.toml，使用 WORKBUDDY_API_KEY 环境变量。
model = "global:gpt-6.1-sol"
model_provider = "workbuddy_global"
model_reasoning_effort = "low"

[model_providers.workbuddy_global]
name = "WorkBuddy GPT-6.1 Sol"
base_url = "http://127.0.0.1:8787/v1"
env_key = "WORKBUDDY_API_KEY"
wire_api = "responses"
requires_openai_auth = false
supports_websockets = false
'''
(OUT/'配置示例/codex-gpt6.1-sol.toml').write_text(codex61,encoding='utf-8')
print('接入信息与国内/国际客户端示例已更新；密钥未输出到控制台。')
