"""Loopback-only workbench adapter for the Go WorkBuddy2API gateway."""
from http.server import ThreadingHTTPServer, BaseHTTPRequestHandler
from pathlib import Path
from urllib.request import Request, urlopen
from urllib.error import HTTPError, URLError
from urllib.parse import parse_qs, urlparse
from datetime import datetime, timedelta, timezone
from daily_activity import DailyTasks
from credit_cache import merge_credits
from concurrent.futures import ThreadPoolExecutor
import json, math, os, re, secrets, subprocess, sys, threading, time

ROOT = Path(__file__).resolve().parent.parent
WEB = Path(__file__).resolve().parent
BASE_COMPAT = WEB.parent / 'compat'
os.chdir(ROOT)
os.environ.update(TMP=str(ROOT / 'data'), TEMP=str(ROOT / 'data'), PYTHONUTF8='1', PYTHONIOENCODING='utf-8')
NONCE = secrets.token_urlsafe(32)
LOCK = threading.RLock()
CREDIT_LOCK = threading.Lock()
FLOW = None
TOOL = {'state': 'idle'}
PORT = 7864
TZ = timezone(timedelta(hours=8))
TASKS = [
 ('checkin', '每日签到', '每日签到与余额查询，积分恢复后解除冷却。', True),
 ('activity', '国内活跃上报', '国内任务中心的活跃上报；国际账号使用真实日常任务。', True),
 ('travel', '猫猫旅行', '自动领养、派出与领奖，按账号进度推进。', True),
 ('keepalive', 'Token 保活', '刷新账号凭证，连续失效 3 次才禁用。', False),
 ('school', '开学季任务', '任务点亮、领奖与抽奖；活动下线时跳过。', True),
 ('cat', '夜猫子任务', '23:00–08:00 窗口内补一次 black_cat 任务。', True),
]

def read_json(path, default=None):
    try: return json.loads(Path(path).read_text(encoding='utf-8-sig'))
    except (FileNotFoundError, ValueError): return default

def write_json(path, value):
    path = Path(path)
    temporary = path.with_suffix(path.suffix + '.tmp')
    temporary.write_text(json.dumps(value, ensure_ascii=False, indent=2), encoding='utf-8')
    os.replace(temporary, path)

def config():
    return read_json(ROOT / 'config.json')

_CRYPTO = None

def crypto_api():
    """按需加载 compat 的 at-rest 解密接口；缺失依赖时给出中文可操作提示。"""
    global _CRYPTO
    if _CRYPTO is not None: return _CRYPTO
    compat = ROOT / 'compat'
    if not compat.is_dir(): compat = BASE_COMPAT
    compat = str(compat)
    if compat not in sys.path: sys.path.insert(0, compat)
    try:
        from core.workbuddy_atrest_crypto import decrypt_auth_field, is_encrypted_field
    except ImportError:
        raise ValueError('无法加载登录文件解密模块：请先安装依赖 cryptography（pip install cryptography）后重试。') from None
    _CRYPTO = (decrypt_auth_field, is_encrypted_field)
    return _CRYPTO

def decrypt_import_field(value, label):
    """WorkBuddy 5.6+ 的 $wbEncrypted 信封字段 → 明文；旧版明文字段原样返回。"""
    decrypt_auth_field, is_encrypted_field = crypto_api()
    if not is_encrypted_field(value): return value
    try:
        plain = decrypt_auth_field(value)
    except Exception as error:
        if 'cryptography' in str(error).lower():
            raise ValueError('无法解密登录文件：缺少 cryptography 库，请执行 pip install cryptography 后重试。') from None
        raise ValueError(f'无法解密登录文件中的{label}：请确认本机已安装 WorkBuddy 5.6+（或设置 WORKBUDDY_ELECTRON_PATH 指向 WorkBuddy 可执行文件），并重新从客户端导出 .info 文件。') from None
    if not isinstance(plain, str) or not plain:
        raise ValueError(f'登录文件中的{label}解密结果无效，请重新导出 .info 文件。')
    return plain

def gateway(path, body=None, timeout=8, headers=None):
    cfg = config()
    request = Request('http://' + cfg['listen'] + path,
                      data=None if body is None else json.dumps(body).encode(),
                      headers={'Authorization':'Bearer ' + cfg['api_key'], 'Content-Type':'application/json', **(headers or {})})
    try:
        with urlopen(request, timeout=timeout) as response:
            return json.loads(response.read())
    except HTTPError as error:
        response = json.loads(error.read())
        if path == '/healthz': return response
        message = response.get('error', response)
        if isinstance(message, dict): message = message.get('message', json.dumps(message, ensure_ascii=False))
        raise ValueError(str(message)) from None

def redact(text):
    for path in (ROOT / 'auths').glob('workbuddy-*.json'):
        doc = read_json(path, {})
        for field in ('accessToken', 'refreshToken'):
            secret = doc.get('auth', {}).get(field)
            if secret: text = text.replace(secret, '[凭据已隐藏]')
    return text.replace(config()['api_key'], '[API Key 已隐藏]')

def account_documents():
    out = []
    for path in sorted((ROOT / 'auths').glob('workbuddy-*.json')):
        doc = read_json(path, {})
        a, c = doc.get('auth', {}), doc.get('account', {})
        domain = str(a.get('domain', ''))
        realm = a.get('realm') or ('global' if domain == 'workbuddy.ai' or domain.endswith('.workbuddy.ai') else 'cn')
        out.append({'uid':c.get('uid'), 'nickname':c.get('nickname') or '未命名账号', 'realm':realm, 'expires_at':a.get('expiresAt', 0)})
    return out

USAGE_PATH = ROOT / 'data' / 'usage-daily.json'
USAGE_LINE = re.compile(r'^\| #\d+ \| (\d{2}:\d{2}:\d{2}) \|')
USAGE_FIELDS = {
    'tok': re.compile(r'\btok=(\d+)\b'),
    'total': re.compile(r'\btotal=([\d.]+)s\b'),
    'credit': re.compile(r'\bcredit=(-|[\d.]+(?:[eE][+-]?\d+)?)\b'),
}

def _blank_bucket():
    return {'requests': 0, 'ok': 0, 'tokens': 0, 'credits': 0.0, 'credit_n': 0,
            'latency_sum': 0.0, 'latency_n': 0}

def _add_to_bucket(bucket, tokens, credit, latency, ok):
    """credit 为 None 表示该行日志没带 credit 字段（旧日志/未观测），不计入积分统计。

    credit_n 记录"观测到积分的请求数"，用来区分「真的花了 0 积分」与「根本没观测」。
    """
    bucket['requests'] += 1
    bucket['ok'] += 1 if ok else 0
    bucket['tokens'] += tokens
    if credit is not None:
        bucket['credits'] = bucket.get('credits', 0.0) + credit
        # 兼容升级前落盘的 bucket：没有 credit_n 键时按 0 起算。
        bucket['credit_n'] = bucket.get('credit_n', 0) + 1
    if latency > 0:
        bucket['latency_sum'] += latency
        bucket['latency_n'] += 1

def _parse_usage_line(line):
    """解析一行请求表格日志；无法识别时返回 None。"""
    stamp = USAGE_LINE.match(line)
    if not stamp: return None
    parts = [p.strip() for p in line.split('|')]
    if len(parts) < 8: return None
    model, status, uid_field = parts[3], parts[5], parts[6]
    if not model or model == '-' or not uid_field.startswith('uid='): return None
    tok = USAGE_FIELDS['tok'].search(line)
    total = USAGE_FIELDS['total'].search(line)
    credit = USAGE_FIELDS['credit'].search(line)
    return {
        'time': stamp.group(1),
        'model': model,
        'status': status,
        'uid': uid_field[4:],
        'tokens': int(tok.group(1)) if tok else 0,
        'latency': float(total.group(1)) if total else 0.0,
        # None = 没有观测到积分（字段缺失或为 "-"），不是"花了 0 积分"。
        'credit': None if not credit or credit.group(1) == '-' else float(credit.group(1)),
    }

def _assign_dates(rows, anchor_date, anchor_time):
    """给只有 HH:MM:SS 的行补日期：从最后一行向前走，时间倒退即跨到前一天。"""
    dates = [None] * len(rows)
    day = anchor_date
    previous = anchor_time
    for index in range(len(rows) - 1, -1, -1):
        current = rows[index]['time']
        if current > previous:
            day = (datetime.strptime(day, '%Y-%m-%d') - timedelta(days=1)).strftime('%Y-%m-%d')
        dates[index] = day
        previous = current
    return dates

def _ingest_usage_log():
    """增量解析网关请求级日志，写入按天 + 按小时聚合的 usage-daily.json。

    日志行只有 HH:MM:SS，没有日期：以文件 mtime 为锚点、逐行倒推补日期
    （时间倒退即跨天）。增量水位记录文件大小 + 已消费行数 + 末行日期，网关
    重启重写日志（行号回 #001、大小变小）时重置水位并跳过该轮，宁可少记不重记。
    """
    path = ROOT / 'logs/server.stdout.log'
    if not path.exists(): return
    try:
        stat = path.stat()
        lines = path.read_text(encoding='utf-8', errors='replace').splitlines()
    except OSError:
        return
    table = [line for line in lines if line.startswith('|')]
    state = read_json(USAGE_PATH, {}) or {}
    cursor = state.get('_cursor') or {}
    identity = str(int(stat.st_size))
    start = 0
    if cursor.get('identity') == identity:
        start = cursor.get('consumed', 0)
    elif cursor and len(table) >= cursor.get('consumed', 0):
        # 大小变了但行数没减：日志继续追加，从上次行号续读。
        start = cursor.get('consumed', 0)
    elif cursor:
        # 网关重启重写了日志，跳过本轮，下一轮重建水位。
        state['_cursor'] = {'identity': identity, 'consumed': len(table)}
        write_json(USAGE_PATH, state)
        return
    fresh = table[start:]
    if not fresh:
        if cursor.get('identity') != identity:
            state['_cursor'] = {'identity': identity, 'consumed': len(table)}
            write_json(USAGE_PATH, state)
        return
    parsed = [row for row in (_parse_usage_line(line) for line in fresh) if row]
    if not parsed:
        state['_cursor'] = {'identity': identity, 'consumed': len(table)}
        write_json(USAGE_PATH, state)
        return
    anchor = datetime.fromtimestamp(stat.st_mtime, TZ)
    anchor_date = anchor.strftime('%Y-%m-%d')
    if cursor.get('identity') == identity and cursor.get('last_date') \
            and cursor.get('last_time') and parsed[0]['time'] >= cursor['last_time']:
        # 续读：沿用上次末尾日期，避免 mtime 漂移造成重复跨天。
        dates = _assign_dates(parsed, cursor['last_date'], cursor['last_time'])
    else:
        dates = _assign_dates(parsed, anchor_date, anchor.strftime('%H:%M:%S'))
    days = state.setdefault('days', {})
    for row, date_key in zip(parsed, dates):
        hour_key = row['time'][:2]
        ok = row['status'] == '200'
        realm = 'global' if row['model'].startswith('global:') else 'cn'
        day = days.setdefault(date_key, {
            'requests': 0, 'ok': 0, 'tokens': 0, 'credits': 0.0,
            'latency_sum': 0.0, 'latency_n': 0, 'hours': {}, 'models': {}, 'accounts': {},
        })
        day.setdefault('hours', {})
        _add_to_bucket(day, row['tokens'], row['credit'], row['latency'], ok)
        _add_to_bucket(day['hours'].setdefault(hour_key, _blank_bucket()),
                       row['tokens'], row['credit'], row['latency'], ok)
        _add_to_bucket(day['models'].setdefault(row['model'], _blank_bucket()),
                       row['tokens'], row['credit'], row['latency'], ok)
        _add_to_bucket(day['accounts'].setdefault(realm + ':' + row['uid'], _blank_bucket()),
                       row['tokens'], row['credit'], row['latency'], ok)
    # 只保留近 90 天
    keep = sorted(k for k in days if not k.startswith('_'))[-90:]
    state['days'] = {k: days[k] for k in keep}
    last = parsed[-1]
    state['_cursor'] = {'identity': identity, 'consumed': len(table),
                        'last_date': dates[-1], 'last_time': last['time']}
    write_json(USAGE_PATH, state)

def _nickname_map():
    """uid 前 8 位 → 昵称；网关日志里 uid 是缩写，auths 里是完整 uuid。"""
    return {a['uid'][:8]: a.get('nickname') or a['uid'][:8] for a in account_documents()}

def _sum_bucket(target, source):
    for key in ('requests', 'ok', 'tokens', 'credits', 'credit_n',
                'latency_sum', 'latency_n'):
        target[key] = target.get(key, 0) + source.get(key, 0)

def _finish_bucket(bucket):
    """补齐可展示字段：平均延迟；积分只在观测到时才给数值（否则 None）。"""
    out = dict(bucket)
    n = out.get('latency_n', 0)
    out['avg_latency'] = round(out.get('latency_sum', 0) / n, 2) if n else 0
    observed = out.get('credit_n', 0)
    out['credits'] = round(out.get('credits', 0), 4) if observed else None
    out['credit_observed'] = observed
    return out

def _day_list(days, keys, nick):
    return [{
        'date': key,
        'requests': days[key].get('requests', 0),
        'ok': days[key].get('ok', 0),
        'tokens': days[key].get('tokens', 0),
        'credits': _finish_bucket(days[key])['credits'],
        'credit_observed': days[key].get('credit_n', 0),
        'avg_latency': _finish_bucket(days[key])['avg_latency'],
        'models': [{'model': m, **_finish_bucket(v)} for m, v in sorted(
            days[key].get('models', {}).items(), key=lambda kv: -kv[1].get('tokens', 0))],
        'accounts': [{'uid': key2.split(':', 1)[1], 'realm': key2.split(':', 1)[0],
                      'nickname': nick.get(key2.split(':', 1)[1], key2.split(':', 1)[1]),
                      **_finish_bucket(v)} for key2, v in sorted(
            days[key].get('accounts', {}).items(), key=lambda kv: -kv[1].get('tokens', 0))],
    } for key in keys]

def daily_usage():
    """近 7 天用量：按天汇总 + 按模型 + 按账号（昵称映射）。"""
    _ingest_usage_log()
    state = read_json(USAGE_PATH, {}) or {}
    days = state.get('days', {})
    nick = _nickname_map()
    keys = sorted(days.keys())[-7:][::-1]
    return _day_list(days, keys, nick)

def usage_report(start=None, end=None):
    """区间用量报表：汇总指标 + 时间序列（按小时或按天）+ 账号/模型明细。"""
    _ingest_usage_log()
    state = read_json(USAGE_PATH, {}) or {}
    days = state.get('days', {})
    nick = _nickname_map()
    if not end:
        end = max(days) if days else datetime.now(TZ).strftime('%Y-%m-%d')
    if not start:
        start = (datetime.strptime(end, '%Y-%m-%d') - timedelta(days=6)).strftime('%Y-%m-%d')
    keys = [k for k in sorted(days) if start <= k <= end]
    total = _blank_bucket()
    accounts, models = {}, {}
    series = []
    hourly = start == end  # 单日看小时曲线，多日看每天一个点

    def _point(bucket):
        """只暴露图表需要的标量字段，别把 hours/models/accounts 子表灌给前端。"""
        done = _finish_bucket(bucket)
        return {k: done[k] for k in ('requests', 'ok', 'tokens', 'credits',
                                     'credit_observed', 'avg_latency')}

    for key in keys:
        day = days[key]
        _sum_bucket(total, day)
        for name, bucket in day.get('accounts', {}).items():
            _sum_bucket(accounts.setdefault(name, _blank_bucket()), bucket)
        for name, bucket in day.get('models', {}).items():
            _sum_bucket(models.setdefault(name, _blank_bucket()), bucket)
    if hourly:
        day = days.get(keys[0]) if keys else None
        for hour in range(24):
            bucket = (day or {}).get('hours', {}).get(f'{hour:02d}') or _blank_bucket()
            series.append({'label': f'{hour:02d}:00', 'date': keys[0] if keys else start,
                           'hour': hour, **_point(bucket)})
    else:
        # 区间内每个自然日都出一个点（含 0），否则曲线会把断档日直接连起来，
        # 看起来像"每天都有量"。
        cursor = datetime.strptime(start, '%Y-%m-%d')
        last = datetime.strptime(end, '%Y-%m-%d')
        while cursor <= last:
            key = cursor.strftime('%Y-%m-%d')
            series.append({'label': key[5:], 'date': key,
                           **_point(days.get(key) or _blank_bucket())})
            cursor += timedelta(days=1)
    summary = _finish_bucket(total)
    summary['success_rate'] = round(total['ok'] / total['requests'] * 100) if total['requests'] else 0
    return {
        'start': start, 'end': end, 'hourly': hourly,
        'summary': summary,
        'series': series,
        'accounts': [{'uid': name.split(':', 1)[1], 'realm': name.split(':', 1)[0],
                      'nickname': nick.get(name.split(':', 1)[1], name.split(':', 1)[1]),
                      **_finish_bucket(b)} for name, b in sorted(
            accounts.items(), key=lambda kv: -kv[1].get('tokens', 0))],
        'models': [{'model': name, **_finish_bucket(b)} for name, b in sorted(
            models.items(), key=lambda kv: -kv[1].get('tokens', 0))],
        'days': _day_list(days, keys[::-1], nick),
    }

def model_usage(limit_bytes=200000):
    """按版本统计近期请求日志里用到的模型（来源：网关请求级表格日志）。"""
    path = ROOT / 'logs/server.stdout.log'
    counts = {}
    if path.exists():
        try:
            with path.open('rb') as log:
                size = path.stat().st_size
                log.seek(max(0, size - limit_bytes))
                text = log.read().decode('utf-8', errors='replace')
        except OSError:
            text = ''
        for line in text.splitlines():
            if not line.startswith('|'):
                continue
            parts = [p.strip() for p in line.split('|')]
            if len(parts) < 7:
                continue
            model, status, ts = parts[3], parts[5], parts[2]
            if not model or model == '-':
                continue
            realm = 'global' if model.startswith('global:') else 'cn'
            row = counts.setdefault((realm, model), {'count':0, 'ok':0, 'last':''})
            row['count'] += 1
            if status == '200':
                row['ok'] += 1
            row['last'] = ts
    out = {'cn': [], 'global': []}
    for (realm, model), row in counts.items():
        out[realm].append({'model':model, 'count':row['count'], 'ok':row['ok'], 'last':row['last']})
    for realm in out:
        out[realm].sort(key=lambda r: (-r['count'], r['model']))
        out[realm] = out[realm][:12]
    return out

def state():
    cfg = config()
    def local_snapshot(path, fallback):
        try: return gateway(path, timeout=1.5)
        except (OSError, ValueError): return fallback
    with ThreadPoolExecutor(max_workers=3) as executor:
        futures = [executor.submit(local_snapshot, path, fallback) for path, fallback in (
            ('/healthz', {'healthy':0}), ('/status', {'accounts':[]}), ('/_manage/task', {'state':'offline'}))]
        health, status, task = [future.result() for future in futures]
    states = {item['uid']:item for item in status.get('accounts', [])}
    accounts = []
    for item in account_documents():
        item.update(states.get(item['uid'], {}))
        item['enabled'] = not item.get('routing_paused', False) and not item.get('disabled', False)
        item['automation_enabled'] = not item.get('disabled', False)
        item['online'] = item['uid'] in states
        accounts.append(item)
    cache_path = ROOT / 'data/workbench-credits.json'
    with CREDIT_LOCK:
        previous = read_json(cache_path, {'ts':None, 'accounts':[]})
        credits = merge_credits(previous, task)
        if credits != previous: write_json(cache_path, credits)
    now = datetime.now(TZ)
    schedules = []
    for name, title, description, cn_only in TASKS:
        hours = cfg['schedule'][name + '_hours']
        enabled = cfg['schedule'][name + '_enabled']
        candidates = []
        for hour in hours:
            candidate = now.replace(hour=hour, minute=0, second=0, microsecond=0)
            if candidate <= now: candidate += timedelta(days=1)
            candidates.append(candidate)
        count = sum(a['automation_enabled'] and (not cn_only or a['realm']=='cn') for a in accounts)
        schedules.append({'name':name, 'title':title, 'description':description, 'hours':hours, 'enabled':enabled, 'cn_only':cn_only, 'eligible':count, 'next':min(candidates).isoformat() if enabled and candidates else None})
    return {'health':health,'status':status,'accounts':accounts,'schedules':schedules,'task':task,'tool':TOOL,
            'credits':credits,'daily_tasks':DAILY.view(accounts),'model_usage':model_usage(),'daily_usage':daily_usage(),'base_url':'http://' + cfg['listen'] + '/v1', 'api_key_hint':cfg['api_key'][:6] + '••••••••' + cfg['api_key'][-4:],
            'settings':{'prompt_mode':cfg['prompt']['mode'], 'prompt_text': (ROOT / 'data/custom-prompt.txt').read_text(encoding='utf-8') if (ROOT / 'data/custom-prompt.txt').exists() else '', 'sanitize':cfg['features']['sanitize_blacklist_fingerprints']},
            'login':{'id':FLOW['id'],'state':FLOW['state'],'realm':FLOW['realm']} if FLOW else None,
            'time':now.isoformat()}

def ensure_idle():
    if TOOL.get('state') == 'running': raise ValueError('辅助工具正在执行，请稍后再试。')
    task = gateway('/_manage/task')
    if task.get('state') == 'running': raise ValueError('有任务正在执行，请稍后再试。')
    if any(a.get('in_flight', 0) for a in gateway('/status').get('accounts', [])):
        raise ValueError('当前有模型请求，请等对话结束再更新设置。')

def _gateway_alive():
    try:
        return gateway('/healthz', timeout=2).get('service') == 'workbuddy2api'
    except (OSError, ValueError):
        return False

def restart_gateway():
    """只重启 7863 网关，保留 8787 兼容层。

    原实现调用 stop.ps1，会连带停掉 8787：改一次设置两个入口同时断，
    客户端正好在空窗期请求就会收到「连接被拒绝」。现在只关闭网关进程，
    启动失败最多重试 3 次，仍失败写 logs/workbench-restart.log 再报错。
    """
    powershell = Path(os.environ['SystemRoot']) / 'System32/WindowsPowerShell/v1.0/powershell.exe'
    # 重启期间挂 pause，避免看门狗把「正在重启」误判成「掉线」后抢跑
    pause = ROOT / 'data/watchdog-pause'
    pause.parent.mkdir(parents=True, exist_ok=True)
    pause.touch()
    try:
        shutdown_ok = False
        try:
            gateway('/_manage/shutdown', {}, timeout=5)
            shutdown_ok = True
        except Exception:
            pass
        # 网关收到 shutdown 后要先 Flush 落盘，实测可拖到 10s；最多等 5s，超时强杀
        deadline = time.time() + 5
        while time.time() < deadline and _gateway_alive():
            time.sleep(0.2)
        if _gateway_alive():
            if not shutdown_ok:
                raise ValueError('网关未能停止（可能有任务正在运行），请稍后再试。')
            try:
                pid = int((ROOT / 'data/server.pid').read_text().strip())
                subprocess.run([str(powershell), '-NoProfile', '-Command', f'Stop-Process -Id {pid} -Force -ErrorAction SilentlyContinue'],
                               timeout=10, creationflags=subprocess.CREATE_NO_WINDOW)
            except (OSError, ValueError):
                pass
            deadline = time.time() + 3
            while time.time() < deadline and _gateway_alive():
                time.sleep(0.2)
            if _gateway_alive():
                raise ValueError('网关未能停止，请稍后再试。')
        last_error = ''
        for attempt in range(3):
            command = [str(powershell), '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', str(ROOT / 'start.ps1'), '-NoLogin']
            # Background Go processes can inherit pipe handles on Windows. Wait on
            # the helper process itself, with no captured pipes held by its child.
            result = subprocess.run(command, cwd=ROOT, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=30, creationflags=subprocess.CREATE_NO_WINDOW)
            if result.returncode == 0 and _gateway_alive():
                return
            last_error = f'attempt {attempt + 1}: exit={result.returncode}'
            time.sleep(1)
        try:
            with (ROOT / 'logs/workbench-restart.log').open('a', encoding='utf-8') as log:
                log.write(f'[{datetime.now(TZ).isoformat()}] gateway restart failed: {last_error}\n')
        except OSError:
            pass
        raise ValueError('服务更新失败，请查看 logs/workbench-restart.log。')
    finally:
        try:
            pause.unlink()
        except OSError:
            pass

def update_config(mutate):
    ensure_idle()
    cfg, old = config(), config()
    mutate(cfg)
    write_json(ROOT / 'config.json', cfg)
    try: restart_gateway()
    except Exception:
        write_json(ROOT / 'config.json', old)
        try: restart_gateway()
        except Exception: pass
        raise
    return {'ok':True}

def save_credential(doc, realm):
    if not isinstance(doc, dict) or not isinstance(doc.get('auth'), dict) or not isinstance(doc.get('account'), dict):
        raise ValueError('需要包含 auth 和 account 对象的完整登录文件。')
    a, c = doc['auth'], doc['account']
    # WorkBuddy 5.6+ 导出的是 $wbEncrypted 信封；校验/落盘前先解成明文（落盘仍为明文）。
    a['accessToken'] = decrypt_import_field(a.get('accessToken'), 'accessToken')
    a['refreshToken'] = decrypt_import_field(a.get('refreshToken'), 'refreshToken')
    if 'nickname' in c: c['nickname'] = decrypt_import_field(c.get('nickname'), '昵称')
    expiry=a.get('expiresAt')
    if isinstance(expiry,(int,float)) and not isinstance(expiry,bool) and math.isfinite(expiry) and 1000000000000<expiry<=32503680000000:
        a['expiresAt']=int(expiry//1000)
    uid = str(c.get('uid', ''))
    if not re.fullmatch(r'[A-Za-z0-9_-]{1,160}', uid): raise ValueError('账号 ID 格式不正确。')
    for field in ('accessToken', 'refreshToken'):
        if not isinstance(a.get(field), str) or not a[field] or len(a[field])>65536: raise ValueError('凭证不完整，请重新登录。')
    if not isinstance(a.get('expiresAt'), (int, float)) or isinstance(a['expiresAt'], bool) or not math.isfinite(a['expiresAt']) or not 0<a['expiresAt']<32503680000: raise ValueError('缺少有效期时间戳。')
    for field in ('nickname','enterpriseId'):
        value=c.get(field,'')
        if not isinstance(value,str) or len(value)>1000 or '\r' in value or '\n' in value: raise ValueError('账号信息包含无效字段。')
    domain = a.get('domain', '')
    if not isinstance(domain, str) or '\r' in domain or '\n' in domain: raise ValueError('账号域名无效。')
    inferred = 'global' if domain=='workbuddy.ai' or domain.endswith('.workbuddy.ai') else 'cn'
    if domain and inferred != realm: raise ValueError('选择的账号版本与登录文件域名不一致。')
    a['realm'] = realm
    path=ROOT / 'auths' / ('workbuddy-' + uid + '.json')
    previous=path.read_bytes() if path.exists() else None
    write_json(path, doc)
    try: gateway('/_manage/reload', {})
    except Exception:
        if previous is None: path.unlink(missing_ok=True)
        else: path.write_bytes(previous)
        raise
    return {'uid':uid,'nickname':c.get('nickname'),'ok':True}

def start_login(realm):
    global FLOW
    if realm not in ('global','cn'): raise ValueError('请选择有效的账号版本。')
    if FLOW and FLOW['state']=='pending' and time.time()-FLOW['started']<600: raise ValueError('已有登录流程，请先完成或取消当前登录。')
    result = subprocess.run([str(ROOT / 'login.exe'), '--realm='+realm, 'url'], capture_output=True, text=True, encoding='utf-8', timeout=35, creationflags=subprocess.CREATE_NO_WINDOW)
    if result.returncode: raise ValueError('无法生成授权链接：'+redact(result.stderr)[-300:])
    url = result.stdout.strip()
    parsed = urlparse(url)
    host = (parsed.hostname or '').lower()
    # CN 的 Origin/Referer 是 codebuddy.cn，但授权链接 host 恒为 copilot.tencent.com；
    # global 为 workbuddy.ai 家族。校验 https、host、端口、路径，并核对 login.exe 落盘的 state。
    expected = 'workbuddy.ai' if realm=='global' else 'copilot.tencent.com'
    host_ok = host==expected or (realm=='global' and host.endswith('.'+expected))
    state = parse_qs(parsed.query).get('state', [''])
    stored = read_json(ROOT / 'data' / 'wb2api-login-state.json', {}) or {}
    state_ok = (isinstance(stored, dict) and isinstance(stored.get('state'), str)
                and stored.get('state') == state[0] and stored.get('realm', realm) == realm)
    try: port_ok = parsed.port in (None, 443)
    except ValueError: port_ok = False
    if (parsed.scheme!='https' or not host_ok or not port_ok
            or parsed.username or parsed.password or parsed.path!='/login'
            or len(state)!=1 or not 1<=len(state[0])<=2048 or not state_ok):
        raise ValueError('授权链接域名或 state 校验失败，请重新生成登录链接。')
    FLOW = {'id':secrets.token_hex(12),'realm':realm,'url':url,'state':'pending','started':time.time()}
    return {'id':FLOW['id'],'url':url,'state':'pending'}

def poll_login(flow_id):
    if not FLOW or FLOW['id']!=flow_id: raise ValueError('登录流程已失效，请重新生成链接。')
    if FLOW['state']!='pending': return {'state':FLOW['state']}
    if time.time()-FLOW['started']>600: FLOW['state']='expired'; return {'state':'expired'}
    result = subprocess.run([str(ROOT / 'login.exe'), '--realm='+FLOW['realm'], 'poll'], capture_output=True, text=True, encoding='utf-8', timeout=35, creationflags=subprocess.CREATE_NO_WINDOW)
    if result.returncode: return {'state':'pending'}
    value = json.loads(result.stdout)
    doc = {'account':{'uid':value.get('uid'), 'nickname':value.get('nickname'), 'enterpriseId':value.get('enterprise_id','')}, 'auth':{'accessToken':value.get('access_token'), 'refreshToken':value.get('refresh_token'), 'expiresAt':int(time.time())+value.get('expires_in',0), 'domain':value.get('domain',''), 'realm':FLOW['realm']}}
    saved = save_credential(doc, FLOW['realm'])
    FLOW['state']='completed'
    return {**saved,'state':'completed'}

def auxiliary(name):
    global TOOL
    ensure_idle()
    if name=='trial' and not any(a['realm']=='global' for a in account_documents()): raise ValueError('试用加油包需要国际版账号。')
    TOOL={'state':'running','name':name}
    def run():
        global TOOL
        try:
            command=[str(ROOT/'trial.exe')] if name=='trial' else [sys.executable,str(ROOT/'scripts/task_runner.py'),'ALL']
            result=subprocess.run(command, cwd=ROOT,capture_output=True,text=True,encoding='utf-8',errors='replace',timeout=180,creationflags=subprocess.CREATE_NO_WINDOW)
            output=redact(result.stdout+'\n'+result.stderr)
            (ROOT/('logs/workbench-'+name+'.log')).write_text(output,encoding='utf-8')
            TOOL={'state':'completed' if result.returncode==0 else 'failed','name':name,'output':output[-6000:]}
        except Exception as error: TOOL={'state':'failed','name':name,'error':redact(str(error))}
    threading.Thread(target=run,daemon=True).start()
    return TOOL

def region(body, save=False):
    uid=body.get('uid')
    if not isinstance(uid,str) or not re.fullmatch(r'[A-Za-z0-9_-]{1,160}',uid): raise ValueError('账号 ID 无效。')
    if not any(a['uid']==uid and a['realm']=='global' for a in account_documents()): raise ValueError('请选择国际版账号。')
    if save and body.get('country') not in ('HK','MO','SG','TH','PH','MY','ID'): raise ValueError('请选择有效地区。')
    sys.path.insert(0,str(ROOT/'scripts')) if str(ROOT/'scripts') not in sys.path else None
    import global_region
    token=read_json(ROOT/'auths'/('workbuddy-'+uid+'.json'))['auth']['accessToken']
    ok,countries,message=global_region.fetch_countries()
    if not ok: raise ValueError('无法读取上游地区列表：'+str(message))
    if save:
        ensure_idle()
        pick=next((c for c in countries if c.get('IOS2')==body['country']),None)
        if pick is None: raise ValueError('上游未提供所选地区。')
        ok,message=global_region.complete_flow(token,uid,pick=pick,call_register=False)
        if not ok: raise ValueError(str(message))
        return {'ok':True,'message':str(message)}
    ok,code,name,message=global_region.detect_region(token)
    return {'countries':[{'code':c['IOS2'],'name':c.get('Name') or c.get('EnName') or c['IOS2']} for c in countries], 'current':code if ok else '', 'message':str(message)}

class Handler(BaseHTTPRequestHandler):
    def log_message(self, format, *args): pass
    def send(self, value, status=200, mime='application/json; charset=utf-8'):
        content=value if isinstance(value,bytes) else json.dumps(value,ensure_ascii=False).encode()
        self.send_response(status)
        self.send_header('Content-Type',mime)
        self.send_header('Cache-Control','no-store')
        self.send_header('X-Content-Type-Options','nosniff')
        self.send_header('X-Frame-Options','DENY')
        self.send_header('Referrer-Policy','no-referrer')
        self.send_header('Content-Security-Policy',"default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'")
        self.send_header('Content-Length',str(len(content)))
        self.end_headers()
        try: self.wfile.write(content)
        except (ConnectionResetError,BrokenPipeError): pass
    def allowed_host(self):
        return self.headers.get('Host') in ('127.0.0.1:7864','localhost:7864')
    def authorized(self):
        origin=self.headers.get('Origin')
        return self.allowed_host() and self.headers.get('X-Workbench-Token')==NONCE and self.headers.get('Sec-Fetch-Site')!='cross-site' and (not origin or origin=='http://'+self.headers['Host'])
    def do_GET(self):
        if not self.allowed_host(): self.send({'error':'invalid host'},403);return
        if self.path=='/favicon.ico': self.send(b'',204,'image/x-icon');return
        if self.path in ('/','/admin/','/admin'):
            self.send((WEB/'index.html').read_text(encoding='utf-8').replace('__TOKEN__',NONCE).encode(),mime='text/html; charset=utf-8');return
        assets={'/style.css':('style.css','text/css; charset=utf-8'),'/extra.css':('extra.css','text/css; charset=utf-8'),'/app.js':('app.js','application/javascript; charset=utf-8')}
        if self.path in assets:
            file,mime=assets[self.path];self.send((WEB/file).read_bytes(),mime=mime);return
        if not self.authorized(): self.send({'error':'本地工作台会话无效，请刷新页面。'},403);return
        try:
            if self.path=='/api/state': result=state()
            elif self.path.startswith('/api/usage'):
                query=parse_qs(urlparse(self.path).query)
                start=(query.get('start') or [None])[0]
                end=(query.get('end') or [None])[0]
                result=usage_report(start, end)
            elif self.path=='/api/models': result=gateway('/v1/models',timeout=150)
            elif self.path=='/api/logs':
                path=ROOT/'logs/server.stderr.log'
                if path.exists():
                    with path.open('rb') as log: log.seek(max(0,path.stat().st_size-22000));text=log.read().decode('utf-8',errors='replace')
                else: text='暂无运行日志。'
                result={'text':redact(text)}
            else: self.send({'error':'not found'},404);return
            self.send(result)
        except Exception as error: self.send({'error':redact(str(error))},400)
    def do_POST(self):
        if not self.authorized(): self.send({'error':'本地工作台会话无效，请刷新页面。'},403);return
        try:
            length=int(self.headers.get('Content-Length','0'))
            if not 0<=length<=1048576: raise ValueError('请求大小不能超过 1 MB。')
            body=json.loads(self.rfile.read(length) or b'{}')
            if not isinstance(body,dict): raise ValueError('无效请求。')
            with LOCK:
                if self.path=='/api/action':
                    name=body.get('name')
                    if name in ('trial','task_query'): result=auxiliary(name)
                    elif name in [t[0] for t in TASKS]+['credits']: result=gateway('/_manage/task',{'name':name})
                    else: raise ValueError('未知任务。')
                elif self.path=='/api/region/info': result=region(body)
                elif self.path=='/api/region/save': result=region(body,save=True)
                elif self.path=='/api/daily-tasks': result=DAILY.configure(body)
                elif self.path=='/api/daily-tasks/resume': result=DAILY.resume(body.get('uid'))
                elif self.path=='/api/account':
                    uid=body.get('uid')
                    if not any(a['uid']==uid for a in account_documents()): raise ValueError('账号不存在。')
                    if not isinstance(body.get('enabled'),bool): raise ValueError('无效的账号状态。')
                    result=gateway('/_manage/account',{'uid':uid,'enabled':body['enabled']})
                elif self.path=='/api/login/start': result=start_login(body.get('realm','global'))
                elif self.path=='/api/login/poll': result=poll_login(body.get('id'))
                elif self.path=='/api/login/cancel':
                    if FLOW and FLOW['id']==body.get('id'): FLOW['state']='cancelled'
                    result={'ok':True}
                elif self.path=='/api/import':
                    if body.get('realm') not in ('cn','global'): raise ValueError('请选择账号版本。')
                    result=save_credential(body.get('document'),body['realm'])
                elif self.path=='/api/schedules':
                    schedule=body.get('schedule',{})
                    def mutate(cfg):
                        for name,*_ in TASKS:
                            item=schedule.get(name)
                            if not isinstance(item,dict) or not isinstance(item.get('enabled'),bool): raise ValueError('需要提供完整的任务设置。')
                            hours=item.get('hours')
                            if not isinstance(hours,list) or not hours or len(hours)>24 or any(type(h)!=int or not 0<=h<=23 for h in hours): raise ValueError('时间须为 0–23 的整点，至少一个。')
                            cfg['schedule'][name+'_enabled']=item['enabled'];cfg['schedule'][name+'_hours']=sorted(set(hours))
                    result=update_config(mutate)
                elif self.path=='/api/settings':
                    mode=body.get('prompt_mode');text=body.get('prompt_text','');sanitize=body.get('sanitize')
                    if mode not in ('passthrough','custom') or not isinstance(text,str) or len(text)>20000 or not isinstance(sanitize,bool): raise ValueError('无效的提示词设置。')
                    if mode=='custom' and not text.strip(): raise ValueError('自定义提示词不能为空。')
                    def mutate(cfg):
                        (ROOT/'data/custom-prompt.txt').write_text(text,encoding='utf-8')
                        cfg['prompt']={'mode':mode,'file':'./data/custom-prompt.txt' if mode=='custom' else ''}
                        cfg['features']['sanitize_blacklist_fingerprints']=sanitize
                    result=update_config(mutate)
                elif self.path=='/api/key': result={'key':config()['api_key']}
                elif self.path=='/api/test':
                    model=body.get('model','global:fast-model');prompt=body.get('prompt','Reply only OK.')
                    if not isinstance(model,str) or len(model)>100 or not model.startswith(('global:','cn:')) or not isinstance(prompt,str) or not 0<len(prompt)<=2000: raise ValueError('模型或消息格式无效。')
                    start=time.time();result=gateway('/v1/chat/completions',{'model':model,'messages':[{'role':'user','content':prompt}],'stream':False,'max_tokens':64},timeout=150);result['elapsed']=round(time.time()-start,2)
                else: self.send({'error':'not found'},404);return
            self.send(result)
        except Exception as error: self.send({'error':redact(str(error))},400)

def automatic_credit_refresh():
    time.sleep(3)
    while True:
        try:
            with LOCK:
                if gateway('/_manage/task').get('state')!='running': gateway('/_manage/task',{'name':'credits'})
        except Exception: pass
        # 风控降频：原 300s（5 分钟一轮 → 288 次/天/号）过高，与高频 chat 叠加易触发上游
        # 11140 账号级封禁（2026-10-22 4 个 global 号被封）。降到 1800s（30 分钟 → 48 次/天/号）。
        time.sleep(1800)

def daily_accounts():
    states={a['uid']:a for a in gateway('/status').get('accounts',[])}
    return [{**a,**states.get(a['uid'],{}),'online':a['uid'] in states,'enabled':not states.get(a['uid'],{}).get('routing_paused',False) and not states.get(a['uid'],{}).get('disabled',True), 'automation_enabled':not states.get(a['uid'],{}).get('disabled',True)} for a in account_documents()]

DAILY=DailyTasks(ROOT,daily_accounts,lambda uid,body:gateway('/_manage/daily-chat',body,timeout=150,headers={'X-Workbench-Account-UID':uid}),redact=redact,pricing=lambda uid:gateway('/_manage/model-pricing?uid='+uid,timeout=12)['models'])

def automatic_daily_tasks():
    while True:
        try: DAILY.tick()
        except Exception as error: print('daily task worker: '+redact(str(error))[:300],flush=True)
        time.sleep(20)

if __name__=='__main__':
    server=ThreadingHTTPServer(('127.0.0.1',PORT),Handler)
    threading.Thread(target=automatic_credit_refresh,daemon=True).start()
    threading.Thread(target=automatic_daily_tasks,daemon=True).start()
    print('WorkBuddy 本地工作台：http://127.0.0.1:7864',flush=True)
    server.serve_forever()
