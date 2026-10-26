"""Account-specific daily real tasks; no simulated activity reports or reward claims."""
from datetime import datetime,timedelta,timezone
from pathlib import Path
import json,os,secrets,threading
from economy_models import economy_candidates,model_unavailable

TZ=timezone(timedelta(hours=8))
WINDOW=(6,7)
POLICY_URL='https://www.workbuddy.ai/document/acceptable-use-policy'
REWARD_URL='https://www.workbuddy.ai/pricing/'

class DailyTasks:
    def __init__(self,root,accounts,call,redact=lambda s:s,clock=None,randbelow=secrets.randbelow,pricing=None):
        self.root=Path(root);self.accounts=accounts;self.call=call;self.redact=redact;self.pricing=pricing
        self.clock=clock or (lambda:datetime.now(TZ));self.randbelow=randbelow;self.lock=threading.RLock()
        self.settings_path=self.root/'data/global-daily-tasks.json';self.journal_path=self.root/'data/global-daily-journal.json'
        self.settings=self.read(self.settings_path,{'accounts':{}});self.journal=self.read(self.journal_path,{'accounts':{}})
        changed=False
        for item in self.journal['accounts'].values():
            for record in item.get('days',{}).values():
                if record.get('state')=='running':
                    record.update(state='interrupted',error='服务中断，执行结果未知；同日不再重发。');item['paused']=True;changed=True
        if changed:self.write(self.journal_path,self.journal)
    @staticmethod
    def read(path,default):
        try:return json.loads(path.read_text(encoding='utf-8'))
        except FileNotFoundError:return default
    @staticmethod
    def write(path,data):
        path.parent.mkdir(parents=True,exist_ok=True);temporary=path.with_suffix('.tmp')
        temporary.write_text(json.dumps(data,ensure_ascii=False,indent=2),encoding='utf-8');os.replace(temporary,path)
    def globals(self):return [a for a in self.accounts() if a['realm']=='global']
    def defaults(self):return {'enabled':False,'prompt':'','model':'global:auto-economy','max_tokens':64}
    def view(self, accounts=None):
        accounts = self.globals() if accounts is None else [a for a in accounts if a['realm']=='global']
        with self.lock:
            today=self.clock().astimezone(TZ).date().isoformat();rows=[]
            for account in accounts:
                uid=account['uid'];s=self.settings['accounts'].get(uid,self.defaults());j=self.journal['accounts'].get(uid,{})
                last=next(reversed(sorted(j.get('days',{}))),None)
                rows.append({'uid':uid,'nickname':account['nickname'],**s,'paused':j.get('paused',False),'today':j.get('days',{}).get(today),'last':j.get('days',{}).get(last) if last else None})
            return {'accounts':rows,'window':'06:00–07:00','timezone':'Asia/Shanghai','policy_url':POLICY_URL,'reward_url':REWARD_URL,'reward_guaranteed':False}
    def configure(self,body):
        rows=body.get('accounts');valid={a['uid'] for a in self.globals()}
        if not isinstance(rows,dict) or not rows or not set(rows)<=valid:raise ValueError('仅能配置现有国际版账号。')
        updates={}
        for uid,s in rows.items():
            if not isinstance(s,dict) or type(s.get('enabled')) is not bool:raise ValueError('无效的任务开关。')
            prompt=s.get('prompt','');limit=s.get('max_tokens');model=s.get('model')
            if not isinstance(prompt,str) or len(prompt)>2000 or (s['enabled'] and not prompt.strip()):raise ValueError('启用前请填写每天要处理的实际任务。')
            if type(limit) is not int or not 32<=limit<=256:raise ValueError('回复 Token 上限须为 32–256。')
            if model not in ('global:auto-economy','global:fast-model','global:default-model'):raise ValueError('请选择有效的国际版模型。')
            updates[uid]={'enabled':s['enabled'],'prompt':prompt.strip(),'max_tokens':limit,'model':model}
        with self.lock:
            self.settings['accounts'].update(updates);self.write(self.settings_path,self.settings)
        self.plan()
        return {'ok':True}
    def resume(self,uid):
        valid={a['uid'] for a in self.globals()}
        with self.lock:
            if uid not in valid:raise ValueError('请选择国际版账号。')
            if not self.settings['accounts'].get(uid,{}).get('prompt'):raise ValueError('请先填写实际任务。')
            item=self.journal['accounts'].setdefault(uid,{'days':{}});item['paused']=False;self.write(self.journal_path,self.journal)
        return {'ok':True,'message':'已恢复后续排程；已尝试的日期不重发。'}
    def plan(self):
        accounts=self.globals()
        now=self.clock().astimezone(TZ);changed=False
        with self.lock:
            for account in accounts:
                uid=account['uid'];s=self.settings['accounts'].get(uid,self.defaults())
                if not s['enabled'] or not s['prompt'].strip():continue
                item=self.journal['accounts'].setdefault(uid,{'paused':False,'days':{}});days=item['days']
                for record in days.values():
                    if record['state']=='pending' and datetime.fromisoformat(record['scheduled_at']).replace(hour=WINDOW[1],minute=0,second=0)<=now:
                        record.update(state='missed',error='错过执行窗口，当日不补发。');changed=True
                date=now.date()+timedelta(days=1 if now.hour>=WINDOW[1] else 0);key=date.isoformat()
                if key not in days and not item.get('paused'):
                    base=datetime.combine(date,datetime.min.time(),tzinfo=TZ).replace(hour=WINDOW[0])
                    slot=base+timedelta(seconds=self.randbelow((WINDOW[1]-WINDOW[0])*3600))
                    days[key]={'state':'pending','scheduled_at':slot.isoformat(),'date':key};changed=True
                while len(days)>35:days.pop(sorted(days)[0]);changed=True
            if changed:self.write(self.journal_path,self.journal)
    def tick(self):
        self.plan();now=self.clock().astimezone(TZ)
        if not WINDOW[0]<=now.hour<WINDOW[1]:return
        for account in self.globals():
            uid=account['uid'];today=now.date().isoformat()
            with self.lock:
                s=self.settings['accounts'].get(uid,self.defaults());j=self.journal['accounts'].get(uid,{})
                record=j.get('days',{}).get(today)
                if not s['enabled'] or not s['prompt'].strip() or j.get('paused') or not record or record['state']!='pending':continue
                if datetime.fromisoformat(record['scheduled_at'])>now:continue
                if not account.get('online') or not account.get('automation_enabled',account.get('enabled')) or account.get('cooling') or account.get('in_flight',0):continue
                record.update(state='running',started_at=now.isoformat());self.write(self.journal_path,self.journal)
                body={'model':s['model'],'messages':[{'role':'user','content':s['prompt']}],'stream':False,'max_tokens':s['max_tokens']}
            try:
                if s['model']=='global:auto-economy':
                    if self.pricing is None:raise ValueError('无法获取模型倍率，已暂停；不会盲选付费模型。')
                    choices=economy_candidates(self.pricing(uid))
                    if not choices:raise ValueError('没有确认免费或倍率不超过 0.34 的可用模型，已暂停。')
                    if choices[0]['multiplier']==0:
                        paid=next((c for c in choices if c['multiplier']>0),None)
                        choices=[choices[0]]+([paid] if paid else choices[1:2])
                    for index,selected in enumerate(choices[:2]):
                        actual={**body,'model':selected['model'],'reasoning_effort':selected['reasoning_effort']}
                        with self.lock:
                            record.update(model=selected['model'],advertised_multiplier=selected['multiplier'],attempt=index+1)
                            self.write(self.journal_path,self.journal)
                        try:result=self.call(uid,actual);break
                        except Exception as error:
                            if index==0 and len(choices)>1 and model_unavailable(error):continue
                            raise
                else:result=self.call(uid,body)
                choice=result.get('choices',[{}])[0];answer=choice.get('message',{}).get('content','')
                if not isinstance(answer,str) or not answer.strip():raise ValueError('上游未返回正文，任务已暂停。')
                output=self.root/'output/daily-tasks'/(today+'-'+uid+'.md');output.parent.mkdir(parents=True,exist_ok=True)
                output.write_text('# '+account['nickname']+' · '+today+'\n\n'+answer+'\n',encoding='utf-8')
                with self.lock:record.update(state='completed',finished_at=self.clock().isoformat(),usage=result.get('usage',{}),truncated=choice.get('finish_reason')=='length',output_file=str(output))
            except Exception as error:
                with self.lock:record.update(state='failed',finished_at=self.clock().isoformat(),error=self.redact(str(error))[:500]);j['paused']=True
            finally:
                with self.lock:self.write(self.journal_path,self.journal)
            now=self.clock().astimezone(TZ)
            if not WINDOW[0]<=now.hour<WINDOW[1]:break
