'use strict';
const $=id=>document.getElementById(id);
const token=document.querySelector('meta[name="workbench-token"]').content;
let data=null,page='dashboard',taskDirty=false,settingsDirty=false,flowId=null,loginTimer=null,toastTimer=null,refreshing=false;
let lastAccountSignature='',lastScheduleSignature='',lastDailySignature='',lastQuotaSignature='',dailyDirty=false;
const titles={dashboard:['WORKSPACE OVERVIEW','让连接，有条不紊','账号、积分与自动任务，都在一个工作空间。'],accounts:['ACCOUNT DIRECTORY','账号管理','集中管理国内与国际版账号、积分与可用状态。'],usage:['USAGE ANALYTICS','用量，一目了然','按天查看 Token 与积分消耗，定位到账号与模型。'],tasks:['AUTOMATION CENTER','让日常，自动发生','六类积分任务，独立排程、独立开关。'],test:['CONNECTION TEST','验证你的连接','用一条真实请求，检查账号与模型的完整链路。'],settings:['CONNECTION SETTINGS','接入与提示词','复制客户端配置，管理系统提示词与指纹脱敏。'],logs:['RUNNING LOGS','每一步，都有记录','查看网关与定时任务的真实执行状态。']};
const symbols={checkin:'▣',activity:'↗',travel:'♧',keepalive:'◇',school:'⌂',cat:'☾',credits:'↻'};
const esc=value=>String(value??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const number=value=>typeof value==='number'?new Intl.NumberFormat('zh-CN').format(value):'—';
// 北京时间一律用「epoch + 8h 后再取 UTC 字段」算，不依赖浏览器的 Intl 时区数据。
// 原因（实测）：同一台机器上浏览器给出的时间比北京时间早整 8 小时——即 toLocaleString 的
// timeZone:'Asia/Shanghai' 被当成了 UTC 处理（截图里 21:26 显示成 13:26）。手算偏移对
// 任何浏览器/时区设置都成立，也让「更新」这一行不会再被误读成"数据没更新"。
const pad2=n=>String(n).padStart(2,'0');
const bjDate=value=>{if(!value)return null;const d=value instanceof Date?value:new Date(typeof value==='number'?value*1000:value);return isNaN(d.getTime())?null:new Date(d.getTime()+8*3600*1000);};
const formatDate=value=>{const d=bjDate(value);return d?`${pad2(d.getUTCMonth()+1)}/${pad2(d.getUTCDate())} ${pad2(d.getUTCHours())}:${pad2(d.getUTCMinutes())}`:'—';};
async function api(path,body){const response=await fetch('/api/'+path,{method:body===undefined?'GET':'POST',headers:{'X-Workbench-Token':token,...(body===undefined?{}:{'Content-Type':'application/json'})},body:body===undefined?undefined:JSON.stringify(body)});const result=await response.json();if(!response.ok)throw new Error(result.error||'操作失败');return result;}
function toast(text){$('toast').textContent=text;$('toast').hidden=false;clearTimeout(toastTimer);toastTimer=setTimeout(()=>$('toast').hidden=true,4500);}
function error(text){$('load-error').textContent=text;$('load-error').hidden=false;toast(text);}
async function work(button,callback){if(button)button.disabled=true;try{return await callback();}catch(e){error(e.message);}finally{if(button)button.disabled=false;}}
function accountPill(a){return !a.online?'<span class="pill neutral">未加载</span>':a.disabled?'<span class="pill warn">系统禁用</span>':a.routing_paused?'<span class="pill neutral">普通调用已排除</span>':a.cooling?'<span class="pill warn">冷却中</span>':'<span class="pill">可用</span>';}
function creditFor(uid){return data?.credits.accounts.find(a=>a.uid===uid&&a.ok)?.remain;}
function creditRow(uid){return (data?.credits?.accounts||[]).find(a=>a.uid===uid);}
function balanceError(row){const raw=row?.error||'';return /http 500|status code: 500/.test(raw)?'上游余额接口异常（HTTP 500）':/401|expired|过期/.test(raw)?'登录凭据可能已过期，请重新登录':/deadline|timeout|超时/.test(raw)?'查询超时，可稍后重试':raw?'余额查询失败，请稍后重试':'';}
function creditBar(remain,used,compact=false){
 const known=Number.isFinite(remain)&&Number.isFinite(used);
 if(!known)return '<div class="credit-unknown">余额尚未查到，暂不计算用量比例</div>';
 const total=Math.max(0,remain)+Math.max(0,used),ratio=total?Math.round(used/total*100):0;
 return `<div class="credit-meter ${compact?'compact':''}"><div class="credit-legend"><span class="spent">已用 ${number(used)}${compact?'':` · ${ratio}%`}</span><span class="remaining">剩余 ${number(remain)}</span></div><progress class="credit-progress ${total===0?'empty':''}" value="${Math.max(0,used)}" max="${total||1}" aria-label="已消耗积分" aria-valuetext="已用 ${used}，剩余 ${remain}"></progress></div>`;
}
function accountCredit(a){const row=creditRow(a.uid);return `${creditBar(row?.ok?row.remain:undefined,row?.ok?row.used:undefined,true)}${row?.error?`<small class="credit-warning" title="${esc(row.error)}">${esc(balanceError(row))}${row.stale?' · 显示上次成功余额':''}</small>`:''}<small class="credit-timestamp">${row?.ok?`余额查询 ${formatDate(row.ts||data.credits.ts)}`:'等待成功查询'}</small>`;}
function renderAccounts(){
 const query=$('account-search').value.toLowerCase(),realmFilter=$('realm-filter').value;
 const signature=JSON.stringify([data.accounts,data.credits,query,realmFilter]);
 if(signature===lastAccountSignature)return;
 lastAccountSignature=signature;
 $('pool-summary').textContent=`已添加 ${data.accounts.length} 个账号 · 可用 ${data.health.healthy||0} · 普通调用已排除 ${data.accounts.filter(a=>a.routing_paused).length}`;
 let visibleCount=0;
 for(const realm of ['cn','global']){
  const list=data.accounts.filter(a=>a.realm===realm),shown=list.filter(a=>`${a.uid} ${a.nickname}`.toLowerCase().includes(query));
  const name=realm==='cn'?'国内版':'国际版',active=list.filter(a=>a.enabled&&!a.disabled).length;
  $(realm+'-account-count').textContent=list.length;
  $(realm+'-account-usage').textContent=`${name}资源池：${active}/${list.length} 个账号参与调用；积分只按实际路由账号使用。`;
  const group=document.querySelector(`[data-account-group="${realm}"]`);group.hidden=(realmFilter!=='all'&&realmFilter!==realm)||(shown.length===0&&query.length>0);
  const body=$('accounts-'+realm+'-body');
  body.innerHTML=shown.map(a=>`<tr><td><div class="account-left"><span class="mini-avatar">${realm==='global'?'W':'C'}</span><div><div class="account-title">${esc(a.nickname)}</div><div class="account-sub mono">${esc(a.uid?.slice(0,8))}…</div></div></div></td><td><div class="account-sub">${accountPill(a)}</div></td><td><label class="checkline account-use"><input type="checkbox" data-account-use="${esc(a.uid)}" ${a.enabled&&!a.disabled?'checked':''} aria-label="${a.enabled&&!a.disabled?'停止':'启用'}${esc(a.nickname)}参与${name}调用">${a.enabled&&!a.disabled?'参与调用':'已排除'}</label></td><td class="account-credit-cell">${accountCredit(a)}</td><td>${formatExpiry(a.expires_at)}${refreshExpiry(a)}</td></tr>`).join('');
  const groupCheck=group.querySelector('[data-select-realm]');
  groupCheck.checked=list.length>0&&active===list.length;groupCheck.indeterminate=active>0&&active<list.length;
  groupCheck.disabled=list.length===0;
  visibleCount+=shown.length;
 }
 $('account-groups').hidden=visibleCount===0;$('accounts-empty').hidden=visibleCount>0;
 document.querySelectorAll('[data-account-use]').forEach(input=>input.onchange=()=>work(input,async()=>{const row=data.accounts.find(a=>a.uid===input.dataset.accountUse);if(!row)return;await api('account',{uid:row.uid,enabled:input.checked});toast(input.checked?'已加入普通调用资源池':'已排除普通调用；签到和凭据刷新继续');lastAccountSignature='';await refresh();}));
 document.querySelectorAll('[data-select-realm]').forEach(input=>input.onchange=()=>work(input,async()=>{const realm=input.dataset.selectRealm;const targets=data.accounts.filter(a=>a.realm===realm&&((a.enabled&&!a.disabled)!==input.checked));for(const account of targets)await api('account',{uid:account.uid,enabled:input.checked});toast(input.checked?'已选用该版本的全部账号':'已将该版本账号全部排除');lastAccountSignature='';await refresh();}));
 $('dashboard-accounts').innerHTML=data.accounts.length?data.accounts.slice(0,3).map(a=>`<div class="dashboard-account"><div class="account-left"><span class="mini-avatar">${a.realm==='global'?'W':'C'}</span><div><div class="account-title">${esc(a.nickname)}</div><div class="account-sub">${a.realm==='global'?'国际版 WorkBuddy':'国内版 CodeBuddy'} · ${number(creditFor(a.uid))} 积分</div></div></div>${accountPill(a)}</div>`).join(''):'<div class="task-empty">尚未添加账号。点击“添加账号”开始使用。</div>';
}
function renderQuota(){
 const usage=data.model_usage||{cn:[],global:[]};
 const signature=JSON.stringify([data.accounts,data.credits.accounts,usage]);
 if(signature!==lastQuotaSignature){
 const expanded=[...$('quota-cards').querySelectorAll('details[open]')].map(el=>el.dataset.creditRealm);
 lastQuotaSignature=signature;
 $('quota-cards').innerHTML=[['cn','国内版 CodeBuddy'],['global','国际版 WorkBuddy']].map(([realm,label])=>{
  const accounts=data.accounts.filter(a=>a.realm===realm),active=accounts.filter(a=>a.enabled&&!a.disabled).length;
  const balances=accounts.map(a=>creditRow(a.uid)),known=balances.filter(c=>c?.ok&&Number.isFinite(c.remain)),failed=balances.filter(c=>c?.error);
  const remain=known.reduce((n,c)=>n+c.remain,0),used=known.reduce((n,c)=>n+(c.used||0),0),capacity=known.reduce((n,c)=>n+(c.size||0),0);
  const models=(usage[realm]||[]).map(r=>`<li><code>${esc(r.model)}</code><span class="quota-model-meta">${r.count} 次${r.ok<r.count?` · ${r.count-r.ok} 失败`:''}</span></li>`).join('')||'<li class="muted">暂无调用记录。</li>';
  const warnings=accounts.filter(a=>creditRow(a.uid)?.error).map(a=>{const row=creditRow(a.uid);return `<li>${esc(a.nickname)}：${esc(balanceError(row))}${row.stale?'；保留上次成功余额':'；未计入合计'}</li>`;}).join('');
  return `<article class="quota-card"><div class="quota-card-head"><div><h3>${label}</h3><p class="muted">参与调用 ${active}/${accounts.length} · 余额已知 ${known.length}/${accounts.length}${failed.length?` · 查询异常 ${failed.length}`:''}</p></div><span class="pill neutral">${realm}:</span></div><div class="quota-numbers"><div><span>剩余积分</span><strong>${known.length?number(remain):'—'}</strong></div><div><span>已消耗积分</span><strong>${known.length?number(used):'—'}</strong></div><div><span>有效套餐总额</span><strong>${known.length?number(capacity):'—'}</strong></div></div>${creditBar(known.length?remain:undefined,known.length?used:undefined)}<p class="quota-note">按已知账号的当前有效套餐统计；套餐到期或续期后会变化${known.length<accounts.length?'。部分账号未查到，合计尚不完整':''}。</p>${warnings?`<details class="credit-errors" data-credit-realm="${realm}"><summary>查看 ${failed.length} 个查询异常</summary><ul>${warnings}</ul></details>`:''}<div class="quota-models"><h4>近期使用的模型</h4><ul>${models}</ul></div></article>`;
 }).join('');
 for(const realm of expanded){const details=$('quota-cards').querySelector(`[data-credit-realm="${realm}"]`);if(details)details.open=true;}
 }
 const q=data.task.name==='credits'?data.task:data.credits.query;
 $('credit-query-status').textContent=q?.state==='running'?`正在查询 ${q.done||0}/${q.total??data.accounts.length}；各账号完成后立即更新`:'查询完成后逐账号保存；失败时保留上次余额。';
 $('credit-query-progress').hidden=q?.state!=='running';
 $('credit-query-progress').max=q?.total||data.accounts.length||1;$('credit-query-progress').value=q?.done||0;
 for(const id of ['quota-refresh','credits-refresh','account-refresh-credit'])$(id).disabled=data.task.state==='running';
}
function taskState(t){return !t.enabled?'<span class="pill neutral">已关闭</span>':t.eligible===0?'<span class="pill warn">等待账号</span>':'<span class="pill">已开启</span>';}
let lastUsageSignature='';
function fmtTokens(n){return n>=1e6?(n/1e6).toFixed(2)+'M':n>=1e3?(n/1e3).toFixed(1)+'k':number(n);}
function renderUsageDaily(){
 const usage=data.daily_usage||[];
 const signature=JSON.stringify(usage);
 if(signature===lastUsageSignature)return;
 lastUsageSignature=signature;
 const body=$('usage-daily-body');
 if(!usage.length){body.innerHTML='<p class="muted">暂无用量记录；发生请求后会自动入账。</p>';return;}
 const today=usage[0];
 const summary=`<div class="usage-today"><div><span>今日请求</span><strong>${number(today.requests)}</strong><small>成功 ${number(today.ok)}${today.requests>today.ok?` · 失败 ${number(today.requests-today.ok)}`:''}</small></div><div><span>今日 Token</span><strong>${fmtTokens(today.tokens)}</strong><small>全部模型合计</small></div><div><span>活跃账号</span><strong>${number(today.accounts.length)}</strong><small>今日发生过请求的账号</small></div></div>`;
 const rows=usage.map(d=>{
  const models=d.models.map(m=>`<li><code>${esc(m.model)}</code><span class="quota-model-meta">${number(m.requests)} 次 · ${fmtTokens(m.tokens)} tok${m.ok<m.requests?` · ${m.requests-m.ok} 失败`:''}</span></li>`).join('');
  const accounts=d.accounts.map(a=>`<li><span class="mini-avatar">${a.realm==='global'?'W':'C'}</span><span>${esc(a.nickname)}</span><span class="quota-model-meta">${number(a.requests)} 次 · ${fmtTokens(a.tokens)} tok</span></li>`).join('');
  return `<details class="usage-day"${d===today?' open':''}><summary><span class="usage-date">${esc(d.date)}</span><span class="usage-day-meta">${number(d.requests)} 次请求 · ${fmtTokens(d.tokens)} tokens · 成功率 ${d.requests?Math.round(d.ok/d.requests*100):0}%</span></summary><div class="usage-detail"><div><h4>按模型</h4><ul>${models||'<li class="muted">无</li>'}</ul></div><div><h4>按账号</h4><ul>${accounts||'<li class="muted">无</li>'}</ul></div></div></details>`;
 }).join('');
 body.innerHTML=summary+`<div class="usage-days">${rows}</div>`;
}
function renderSchedules(){
 $('dashboard-tasks').innerHTML=data.schedules.map(t=>`<div class="task-row"><span class="task-symbol">${symbols[t.name]}</span><strong>${t.title}</strong><span class="task-time">${t.hours.map(h=>String(h).padStart(2,'0')+':00').join(' / ')}</span>${taskState(t)}</div>`).join('');
 if(taskDirty)return;
 const signature=JSON.stringify(data.schedules);
 if(signature===lastScheduleSignature)return;
 lastScheduleSignature=signature;
 $('task-cards').innerHTML=data.schedules.map(t=>`<article class="task-card"><div class="task-card-head"><h3>${symbols[t.name]} ${t.title}</h3><label class="checkline"><input type="checkbox" data-enabled="${t.name}" ${t.enabled?'checked':''} aria-label="启用${t.title}">开启</label></div><p class="task-description">${t.description}</p><span class="pill ${t.cn_only?'neutral':''}">${t.cn_only?'国内版任务中心':'国内 / 国际版'}</span><label for="hours-${t.name}">执行时间 · 北京时间</label><input type="text" id="hours-${t.name}" data-hours="${t.name}" value="${t.hours.join(', ')}" aria-label="${t.title}执行整点"><div class="eligibility">${t.eligible>0?`适用 ${t.eligible} 个已启用账号`:`${t.cn_only?'添加国内版账号后执行':'需要已启用的账号'}`}<br>下次排程：${t.enabled?formatDate(t.next):'已关闭'}</div><button type="button" class="secondary run-task" data-action="${t.name}" ${t.eligible===0?'disabled':''}>立即执行 ↗</button></article>`).join('');
 $('task-cards').querySelectorAll('input').forEach(el=>el.oninput=()=>taskDirty=true);
 $('task-cards').querySelectorAll('[data-action]').forEach(b=>b.onclick=()=>runAction(b.dataset.action,b));
}
function renderState(){
 $('load-error').hidden=true;
 $('sidebar-status').textContent=data.health.service?'网关服务已连接':'网关当前离线';
 $('metric-accounts').textContent=number(data.health.healthy||0);
 const globalCount=data.accounts.filter(a=>a.realm==='global').length,cnCount=data.accounts.filter(a=>a.realm==='cn').length;
 $('metric-realms').textContent=`国际版 ${globalCount} · 国内版 ${cnCount}`;
 for(const realm of ['cn','global']){
  const accounts=data.accounts.filter(a=>a.realm===realm),known=accounts.map(a=>creditFor(a.uid)).filter(n=>typeof n==='number');
  const total=known.reduce((n,v)=>n+v,0),shown=known.length?number(total):'—';
  $('metric-'+realm+'-credits').textContent=shown;$('account-'+realm+'-credit-total').textContent=shown;
  $('metric-'+realm+'-credit-time').textContent=`余额已知 ${known.length}/${accounts.length} · ${accounts.filter(a=>creditRow(a.uid)?.error).length?'存在查询异常':data.credits.ts?formatDate(data.credits.ts):'待首次查询'}`;
 }
 $('metric-tasks').textContent=`${data.schedules.filter(t=>t.enabled).length} / 6`;
 $('metric-requests').textContent=number(data.accounts.reduce((n,a)=>n+(a.success_count||0),0));
 $('dashboard-base').textContent=data.base_url;$('settings-base').textContent=data.base_url;$('key-hint').textContent=data.api_key_hint;
 const task=data.task,title=data.schedules.find(t=>t.name===task.name)?.title||(task.name==='credits'?'积分查询':task.name||'');
 $('live-task').textContent=task.state==='running'?`${title}正在执行${task.name==='credits'?` · 已完成 ${task.done||0}/${task.total??data.accounts.length}`:''}。`:task.state==='completed'?`${title}已执行结束 · ${formatDate(task.finished_at)}。各账号结果请查看日志。`:task.state==='failed'?`任务执行失败：${task.error||'请查看日志'}`:task.state==='offline'?'网关离线，请双击 workbench.cmd 启动。':'当前没有手动任务运行，定时任务将按日程触发。';
 if(!settingsDirty){$('prompt-mode').value=data.settings.prompt_mode;$('custom-prompt').value=data.settings.prompt_text;$('sanitize').checked=data.settings.sanitize;}
 if(data.tool.output||data.tool.error){$('tool-output').hidden=false;$('tool-output').textContent=data.tool.output||data.tool.error;}
 renderAccounts();renderSchedules();renderDaily();renderQuota();renderUsageDaily();
}
async function refresh(){if(refreshing)return;refreshing=true;try{data=await api('state');renderState();}catch(e){error(e.message);}finally{refreshing=false;}}
function showPage(next){page=next;document.querySelectorAll('.page-panel').forEach(el=>el.hidden=el.id!=='page-'+next);document.querySelectorAll('[data-page]').forEach(b=>b.classList.toggle('selected',b.dataset.page===next));const [kicker,title,description]=titles[next];$('page-kicker').textContent=kicker;$('page-title').replaceChildren(document.createTextNode(title));const dot=document.createElement('span');dot.className='title-dot';dot.textContent='.';$('page-title').append(dot);$('page-desc').textContent=description;$('breadcrumb').textContent={dashboard:'工作台概览',tasks:'自动任务',accounts:'账号管理',usage:'用量统计',test:'连接测试',settings:'接入与提示词',logs:'运行日志'}[next];if(next==='logs')loadLogs();if(next==='usage')loadUsage();}
async function runAction(name,button){return work(button,async()=>{await api('action',{name});toast(name==='credits'?'正在查询积分，完成后自动更新。':'任务已启动，执行结果请查看日志。');await refresh();});}
async function loadLogs(){try{const value=await api('logs');$('log-output').textContent=value.text;}catch(e){error(e.message);}}

/* ---------- 用量统计 ---------- */
const usageState={range:'day',start:'',end:'',metric:'tokens',view:'accounts',report:null};
const isoDay=d=>`${d.getFullYear()}-${pad2(d.getMonth()+1)}-${pad2(d.getDate())}`;
function usageRange(){const today=new Date(),end=isoDay(today);
 if(usageState.range==='custom')return{start:usageState.start||end,end:usageState.end||end};
 if(usageState.range==='day')return{start:end,end};
 let from;
 if(usageState.range==='week'){from=new Date(today);from.setDate(from.getDate()-((today.getDay()+6)%7));}  // 本周一
 else if(usageState.range==='month'){from=new Date(today.getFullYear(),today.getMonth(),1);}            // 本月 1 号
 else{from=new Date(today);from.setDate(from.getDate()-6);}                                            // 近 7 天
 return{start:isoDay(from),end};}
async function loadUsage(){const{start,end}=usageRange();$('usage-caption').textContent='正在读取用量…';try{usageState.report=await api(`usage?start=${start}&end=${end}`);renderUsage();}catch(e){error(e.message);}}
function usageFmt(n){if(n>=1e8)return(n/1e8).toFixed(2)+' 亿';if(n>=1e4)return(n/1e4).toFixed(2)+' 万';return number(n);}
function renderUsage(){const r=usageState.report;if(!r)return;const s=r.summary;
 $('usage-requests').textContent=number(s.requests);
 $('usage-requests-sub').textContent=`成功 ${number(s.ok)} · 失败 ${number(s.requests-s.ok)} · 成功率 ${s.success_rate}%`;
 $('usage-tokens').textContent=usageFmt(s.tokens);
 $('usage-tokens-sub').textContent=s.requests?`平均 ${number(Math.round(s.tokens/s.requests))} / 次`:'暂无请求';
 const creditsKnown=s.credits!==null&&s.credits!==undefined;
 const partial=creditsKnown&&s.credit_observed<s.requests;
 $('usage-credits').textContent=creditsKnown?number(s.credits):'—';
 $('usage-credits-sub').textContent=!creditsKnown?'该区间日志未记录积分'
  :partial?`仅 ${number(s.credit_observed)}/${number(s.requests)} 次有积分记录`
  :(s.tokens?`约 ${(s.credits/s.tokens*1000).toFixed(3)} / 千 token`:'暂无消耗');
 $('usage-latency').textContent=s.avg_latency?`${s.avg_latency.toFixed(1)}s`:'—';
 $('usage-latency-sub').textContent=s.requests?'单次请求端到端平均耗时':'暂无请求';
 $('usage-caption').textContent=`${r.start} — ${r.end} · ${r.hourly?'按小时':'按天'} · ${number(r.days.length)} 天有记录`;
 $('usage-chart-note').textContent=r.hourly?'当前为单日视图，按小时展示。':'多日视图，每天一个数据点。';
 renderUsageChart(r);renderUsageTable(r);}
function renderUsageChart(r){const points=r.series.map(p=>({label:p.label,date:p.date,requests:p.requests,value:p[usageState.metric]||0}));
 const box=$('usage-chart');
 const metricName={tokens:'Token',requests:'请求',credits:'积分'}[usageState.metric];
 const unobserved=usageState.metric==='credits'&&!r.series.some(p=>p.credit_observed);
 const partialCredit=usageState.metric==='credits'&&!unobserved&&r.series.some(p=>!p.credit_observed);
 if(!points.length||(unobserved?false:points.every(p=>!p.value))){
  box.innerHTML=`<p class="muted">${unobserved?'该区间日志未记录积分（网关升级前没有该字段）。':`该时间段没有${metricName}记录。`}</p>`;return;}
 $('usage-chart-note').textContent=(r.hourly?'当前为单日视图，按小时展示。':'多日视图，每天一个数据点。')
  +(partialCredit?' 部分日期无积分记录，按 0 显示。':'');
 const W=980,H=280,PL=64,PR=24,PT=24,PB=42;
 const max=Math.max(...points.map(p=>p.value))||1;
 const stepX=points.length>1?(W-PL-PR)/(points.length-1):0;
 const x=i=>PL+stepX*i, y=v=>PT+(H-PT-PB)*(1-v/max);
 // 多日视图里 requests=0 表示当天完全没有请求，折线要在此断开；
 // 否则会把"没有数据的一天"连成斜坡，看起来像那天真的产生了一小截用量。
 // 单日按小时的视图保持连续（小时内的 0 是真实的"这小时没人用"）。
 const gap=p=>!r.hourly&&!(p.requests>0);
 const segments=[];let current=[];
 points.forEach((p,i)=>{if(gap(p)){if(current.length)segments.push(current);current=[];}else current.push(i);});
 if(current.length)segments.push(current);
 if(!segments.length){box.innerHTML=`<p class="muted">该时间段没有${metricName}记录。</p>`;return;}
 // 单点区间（如只选了一天有数据的 2 天窗口）画不出线段，用短横线表示，
 // 否则整张图只剩一个悬空的圆点，看不出数值落在哪。
 const pathOf=seg=>seg.length===1
  ?`M${(x(seg[0])-16).toFixed(1)},${y(points[seg[0]].value).toFixed(1)} L${(x(seg[0])+16).toFixed(1)},${y(points[seg[0]].value).toFixed(1)}`
  :seg.map((i,k)=>`${k?'L':'M'}${x(i).toFixed(1)},${y(points[i].value).toFixed(1)}`).join(' ');
 const line=segments.map(pathOf).join(' ');
 const area=segments.filter(seg=>seg.length>1)
  .map(seg=>`${pathOf(seg)} L${x(seg[seg.length-1]).toFixed(1)},${H-PB} L${x(seg[0]).toFixed(1)},${H-PB} Z`).join(' ');
 const areaTag=area?`<path d="${area}" class="chart-area"/>`:'';
 const ticks=4,grid=[];
 // 积分量级很小（个位/小数），按整数取整会把刻度全压成 0，需要按量级保留小数。
 const tickFmt=v=>max>=100?usageFmt(Math.round(v)):max>=1?v.toFixed(1):v.toFixed(3).replace(/0+$/,'').replace(/\.$/,'');
 for(let i=0;i<=ticks;i++){const v=max*i/ticks,yy=y(v);grid.push(`<line x1="${PL}" y1="${yy}" x2="${W-PR}" y2="${yy}" class="chart-grid"/><text x="${PL-10}" y="${yy+4}" class="chart-axis" text-anchor="end">${tickFmt(v)}</text>`);}
 const labelEvery=Math.max(1,Math.ceil(points.length/(r.hourly?12:10)));
 const labels=points.map((p,i)=>i%labelEvery&&i!==points.length-1?'':`<text x="${x(i)}" y="${H-16}" class="chart-axis" text-anchor="middle">${esc(p.label)}</text>`).join('');
 const dots=points.map((p,i)=>p.value?`<circle cx="${x(i)}" cy="${y(p.value)}" r="3.5" class="chart-dot"><title>${esc(r.hourly?`${p.date} ${p.label}`:p.date)} · ${usageFmt(p.value)}</title></circle>`:'');
 box.innerHTML=`<svg viewBox="0 0 ${W} ${H}" role="img" aria-label="用量趋势图">${grid.join('')}${areaTag}<path d="${line}" class="chart-line"/>${dots.join('')}${labels}</svg>`;}
function renderUsageTable(r){const rows=usageState.view==='accounts'?r.accounts:usageState.view==='models'?r.models.map(m=>({...m,nickname:m.model,uid:m.model,realm:''})):r.days;
 const box=$('usage-table');
 if(!rows.length){box.innerHTML='<p class="muted">暂无数据。</p>';return;}
 if(usageState.view==='days'){
  box.innerHTML=`<table><thead><tr><th>日期</th><th>请求</th><th>成功</th><th>Token</th><th>积分</th><th>平均延迟</th></tr></thead><tbody>${rows.map(d=>`<tr><td class="mono">${esc(d.date)}</td><td>${number(d.requests)}</td><td>${number(d.ok)}</td><td>${usageFmt(d.tokens)}</td><td>${number(d.credits)}</td><td>${d.avg_latency?d.avg_latency.toFixed(1)+'s':'—'}</td></tr>`).join('')}</tbody></table>`;
  return;}
 const nameHeader=usageState.view==='accounts'?'账号':'模型';
 box.innerHTML=`<table><thead><tr><th>${nameHeader}</th><th>请求</th><th>成功</th><th>Token</th><th>积分</th><th>平均延迟</th></tr></thead><tbody>${rows.map(a=>`<tr><td>${usageState.view==='accounts'?`<div class="account-left"><span class="mini-avatar">${a.realm==='global'?'W':'C'}</span><div><div class="account-title">${esc(a.nickname)}</div><div class="account-sub mono">${esc((a.uid||'').slice(0,8))}…</div></div></div>`:`<code>${esc(a.model)}</code>`}</td><td>${number(a.requests)}</td><td>${number(a.ok)}</td><td>${usageFmt(a.tokens)}</td><td>${number(a.credits)}</td><td>${a.avg_latency?a.avg_latency.toFixed(1)+'s':'—'}</td></tr>`).join('')}</tbody></table>`;}
document.addEventListener('click',event=>{const range=event.target.closest('[data-range]');if(range){usageState.range=range.dataset.range;document.querySelectorAll('[data-range]').forEach(b=>b.classList.toggle('selected',b===range));$('usage-custom').hidden=usageState.range!=='custom';if(usageState.range!=='custom')loadUsage();return;}
 const metric=event.target.closest('[data-metric]');if(metric){usageState.metric=metric.dataset.metric;document.querySelectorAll('[data-metric]').forEach(b=>b.classList.toggle('selected',b===metric));if(usageState.report)renderUsageChart(usageState.report);return;}
 const view=event.target.closest('[data-view]');if(view){usageState.view=view.dataset.view;document.querySelectorAll('[data-view]').forEach(b=>b.classList.toggle('selected',b===view));if(usageState.report)renderUsageTable(usageState.report);}});
$('usage-refresh').onclick=()=>work($('usage-refresh'),loadUsage);
$('usage-apply').onclick=()=>{usageState.start=$('usage-start').value;usageState.end=$('usage-end').value;loadUsage();};
async function copy(value){await navigator.clipboard.writeText(value);toast('已复制到剪贴板');}
function openAccount(){ $('account-error').textContent='';$('account-dialog').showModal(); }
function setAccountMode(mode){$('browser-panel').hidden=mode!=='browser';$('file-panel').hidden=mode!=='file';$('mode-browser').setAttribute('aria-pressed',String(mode==='browser'));$('mode-file').setAttribute('aria-pressed',String(mode==='file'));}
async function pollLogin(){if(!flowId)return;try{const result=await api('login/poll',{id:flowId});if(result.state==='completed'){flowId=null;clearTimeout(loginTimer);$('oauth-status').textContent='授权成功，账号已加入账号池。';toast('账号已保存');await refresh();setTimeout(()=>$('account-dialog').close(),900);$('oauth-start').disabled=false;return;}if(result.state==='expired'){flowId=null;$('oauth-status').textContent='链接已过期，请重新生成。';$('oauth-start').disabled=false;return;}$('oauth-status').textContent='正在等待官方页面授权，完成后将自动保存。';loginTimer=setTimeout(pollLogin,3000);}catch(e){$('account-error').textContent=e.message;loginTimer=setTimeout(pollLogin,5000);}}
document.querySelectorAll('[data-page]').forEach(b=>b.onclick=()=>showPage(b.dataset.page));document.querySelectorAll('[data-dashboard-page]').forEach(b=>b.onclick=()=>showPage(b.dataset.dashboardPage));
$('refresh').onclick=()=>work($('refresh'),refresh);$('overview-logs').onclick=()=>showPage('logs');$('reload-logs').onclick=()=>work($('reload-logs'),loadLogs);
$('credits-refresh').onclick=()=>runAction('credits',$('credits-refresh'));$('account-refresh-credit').onclick=()=>runAction('credits',$('account-refresh-credit'));$('quota-refresh').onclick=()=>runAction('credits',$('quota-refresh'));document.querySelectorAll('.tool-buttons [data-action]').forEach(b=>b.onclick=()=>runAction(b.dataset.action,b));
$('account-search').oninput=()=>data&&renderAccounts();$('realm-filter').onchange=()=>data&&renderAccounts();$('add-account').onclick=openAccount;$('dash-add').onclick=openAccount;
$('copy-base').onclick=()=>work($('copy-base'),()=>copy(data.base_url));$('settings-copy-base').onclick=()=>work($('settings-copy-base'),()=>copy(data.base_url));$('copy-key').onclick=()=>work($('copy-key'),async()=>{const value=await api('key',{});await copy(value.key);});
$('mode-browser').onclick=()=>setAccountMode('browser');$('mode-file').onclick=()=>setAccountMode('file');
$('close-account').onclick=()=>$('account-dialog').close();
$('account-dialog').addEventListener('close',()=>{clearTimeout(loginTimer);if(flowId){api('login/cancel',{id:flowId}).catch(()=>{});flowId=null;}$('oauth-start').disabled=false;$('oauth-link-box').hidden=true;});
$('oauth-start').onclick=async()=>{const b=$('oauth-start');b.disabled=true;$('account-error').textContent='';try{const result=await api('login/start',{realm:$('login-realm').value});flowId=result.id;$('oauth-open').href=result.url;$('oauth-link-box').hidden=false;$('oauth-status').textContent='点击“打开官方登录页面”，完成 CLI 授权。';loginTimer=setTimeout(pollLogin,3000);}catch(e){$('account-error').textContent=e.message;b.disabled=false;}};
$('import-account').onclick=async()=>{const b=$('import-account');b.disabled=true;$('account-error').textContent='';try{const file=$('credential-file').files[0];if(!file||file.size>1048576)throw new Error('请选择一个不超过 1 MB 的登录文件。');await api('import',{realm:$('login-realm').value,document:JSON.parse(await file.text())});toast('账号已导入');$('account-dialog').close();await refresh();}catch(e){$('account-error').textContent=e.message;}finally{b.disabled=false;}};
$('schedule-form').onsubmit=event=>{event.preventDefault();const button=event.submitter;work(button,async()=>{const schedule={};data.schedules.forEach(t=>{const raw=document.querySelector(`[data-hours="${t.name}"]`).value.trim();if(!raw||raw.split(/[,，]/).some(v=>!/^\d{1,2}$/.test(v.trim())))throw new Error(`${t.title}请输入 0–23 的整点，以逗号分隔。`);const hours=raw.split(/[,，]/).map(v=>Number(v.trim()));if(hours.some(h=>h<0||h>23))throw new Error('整点只能为 0–23。');schedule[t.name]={enabled:document.querySelector(`[data-enabled="${t.name}"]`).checked,hours};});await api('schedules',{schedule});taskDirty=false;toast('任务设置已保存并生效');await refresh();});};
['prompt-mode','custom-prompt','sanitize'].forEach(id=>$(id).oninput=()=>settingsDirty=true);
$('settings-form').onsubmit=event=>{event.preventDefault();work(event.submitter,async()=>{await api('settings',{prompt_mode:$('prompt-mode').value,prompt_text:$('custom-prompt').value,sanitize:$('sanitize').checked});settingsDirty=false;toast('提示词与脱敏设置已生效');await refresh();});};
$('load-models').onclick=()=>work($('load-models'),async()=>{const result=await api('models');const previous=$('test-model').value;$('test-model').replaceChildren(...result.data.map(m=>{const option=document.createElement('option');option.value=m.id;option.textContent=m.id;return option;}));if(result.data.some(m=>m.id===previous))$('test-model').value=previous;toast(`已读取 ${result.data.length} 个模型名称，实际权限由上游决定。`);});
$('test-form').onsubmit=event=>{event.preventDefault();work($('test-submit'),async()=>{$('test-status').textContent='请求中';$('test-output').textContent='正在等待模型响应…';try{const result=await api('test',{model:$('test-model').value,prompt:$('test-prompt').value});$('test-output').textContent=result.choices?.[0]?.message?.content||JSON.stringify(result,null,2);$('test-status').textContent='连接成功';$('test-meta').textContent=`${result.model} · ${result.elapsed}s · ${result.usage?.total_tokens??'—'} tokens · ${result.usage?.credit??'—'} credit`;await refresh();}catch(e){$('test-status').textContent='请求失败';$('test-output').textContent=e.message;throw e;}});};
refresh();setInterval(()=>{if(!document.hidden)refresh();},2000);

$('region-open').onclick=()=>{const accounts=data.accounts.filter(a=>a.realm==='global');$('region-account').replaceChildren(...accounts.map(a=>{const o=document.createElement('option');o.value=a.uid;o.textContent=a.nickname;return o;}));$('region-message').textContent=accounts.length?'':'请先添加国际版账号。';$('region-country').replaceChildren(new Option('请先查询可选地区',''));$('region-current').textContent='';$('region-save').disabled=true;$('region-dialog').showModal();};
$('region-close').onclick=()=>$('region-dialog').close();
$('region-account').onchange=()=>{$('region-country').replaceChildren(new Option('请先查询可选地区',''));$('region-save').disabled=true;$('region-current').textContent='';};
$('region-load').onclick=()=>work($('region-load'),async()=>{$('region-message').textContent='';const r=await api('region/info',{uid:$('region-account').value});$('region-current').textContent=r.current?'当前地区：'+r.current:'尚未设置地区或上游暂不可查询';$('region-country').replaceChildren(new Option('请选择注册地区',''),...r.countries.map(c=>new Option(c.name+' ('+c.code+')',c.code)));$('region-save').disabled=true;});
$('region-country').onchange=()=>{$('region-save').disabled=!$('region-country').value;};
$('region-save').onclick=()=>work($('region-save'),async()=>{const r=await api('region/save',{uid:$('region-account').value,country:$('region-country').value});$('region-current').textContent=r.message;toast('地区资料已保存，注册激活完成');});

$('copy-compat').onclick=()=>work($('copy-compat'),()=>copy('http://127.0.0.1:8787/v1'));

// 凭据有效期（access token 过期时刻）：同样用 epoch+8h 手算北京时间，理由见 formatDate。
function formatExpiry(value){const d=bjDate(value);return d?`${d.getUTCFullYear()}/${pad2(d.getUTCMonth()+1)}/${pad2(d.getUTCDate())} ${pad2(d.getUTCHours())}:${pad2(d.getUTCMinutes())}`:'—';}

// refreshExpiry 展示 refresh token 自身剩余天数：这是唯一会"永久失效"的倒计时。
// access token 过期随时能刷回来，refresh token 过期只能人工重登——所以两类分开显示，
// 25 天内标黄、已失效标红，和每日 10:00 的桌面提醒口径一致。
function refreshExpiry(a){
 if(a.disabled)return '<small class="credit-warning">凭据已失效，需重新登录</small>';
 const d=a.refresh_days_left;
 if(typeof d!=='number'||d<0)return '<small class="credit-timestamp">剩余天数未知</small>';
 if(d<=25)return `<small class="credit-warning">refresh token 仅剩 ${d} 天，请尽快重登</small>`;
 return `<small class="credit-timestamp">refresh token 剩余 ${d} 天</small>`;
}

function renderDaily(){
 if(dailyDirty||!data.daily_tasks)return;
 const signature=JSON.stringify(data.daily_tasks);if(signature===lastDailySignature)return;lastDailySignature=signature;
 $('daily-note').textContent='默认关闭；填写实际任务后启用。自动省积分按实时倍率选模型，失败后暂停。';
 const states={pending:'等待排程',running:'正在执行',completed:'已完成',failed:'失败，已暂停',interrupted:'中断，已暂停',missed:'错过窗口'};
 $('daily-cards').innerHTML=data.daily_tasks.accounts.map(a=>{
  const r=a.today||a.last;
  return `<article class="task-card" data-daily-uid="${esc(a.uid)}"><div class="task-card-head"><h3>${esc(a.nickname)}</h3><label class="checkline"><input type="checkbox" data-daily-enabled ${a.enabled?'checked':''}>开启</label></div><label>每日实际任务</label><textarea data-daily-prompt rows="4" maxlength="2000" placeholder="填写需要每天处理的实际任务；请勿填刷活跃用的空对话。">${esc(a.prompt)}</textarea><label>模型</label><select data-daily-model><option value="global:auto-economy" ${a.model==='global:auto-economy'?'selected':''}>自动省积分：确认免费优先，其次低倍率（≤0.34）</option><option value="global:fast-model" ${a.model==='global:fast-model'?'selected':''}>global:fast-model</option><option value="global:default-model" ${a.model==='global:default-model'?'selected':''}>global:default-model</option></select><label>回复 Token 上限</label><input type="number" data-daily-limit min="32" max="256" value="${a.max_tokens}"><p class="eligibility">${a.paused?'异常后已暂停':a.enabled?'已启用':'未启用'} · 06:00–07:00<br>${r?`${states[r.state]||r.state} · ${formatDate(r.scheduled_at)}`:'填写任务并启用后生成排程'}${r?.model?`<br>实际模型：${esc(r.model)} · 目录倍率 ${r.advertised_multiplier??'—'}`:''}${r?.error?`<br>${esc(r.error)}`:''}${r?.output_file?`<br>输出：${esc(r.output_file)}`:''}${r?.truncated?'<br>回复达到 Token 上限，可能被截断':''}</p>${a.paused?'<button type="button" class="secondary" data-daily-resume>恢复后续排程</button>':''}</article>`;
 }).join('');
 $('daily-cards').querySelectorAll('input,textarea,select').forEach(el=>el.oninput=()=>dailyDirty=true);
 $('daily-cards').querySelectorAll('[data-daily-resume]').forEach(b=>b.onclick=()=>work(b,async()=>{await api('daily-tasks/resume',{uid:b.closest('[data-daily-uid]').dataset.dailyUid});lastDailySignature='';toast('已恢复后续排程；同日不重发。');await refresh();}));
}
$('daily-form').onsubmit=e=>{e.preventDefault();work(e.submitter,async()=>{
 const accounts={};$('daily-cards').querySelectorAll('[data-daily-uid]').forEach(row=>accounts[row.dataset.dailyUid]={enabled:row.querySelector('[data-daily-enabled]').checked,prompt:row.querySelector('[data-daily-prompt]').value,model:row.querySelector('[data-daily-model]').value,max_tokens:Number(row.querySelector('[data-daily-limit]').value)});
 await api('daily-tasks',{accounts});dailyDirty=false;lastDailySignature='';toast('国际版日常任务已保存。');await refresh();
});};
