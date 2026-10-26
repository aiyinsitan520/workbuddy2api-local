"""Interactive or CLI example for the locally deployed unified gateway."""
import argparse,json,sys
from pathlib import Path
from urllib.request import Request,urlopen
from urllib.error import HTTPError
ROOT=Path(__file__).resolve().parent.parent
def main():
 p=argparse.ArgumentParser();p.add_argument('--realm',choices=['cn','global'],default='global');p.add_argument('--protocol',choices=['chat','responses','messages'],default='chat');p.add_argument('--prompt');p.add_argument('--models',action='store_true');p.add_argument('--interactive',action='store_true');args=p.parse_args()
 cfg=json.loads((ROOT/'config.json').read_text(encoding='utf-8-sig'));key=cfg['api_key']
 if args.interactive:
  args.realm=input('版本 cn / global [global]：').strip() or 'global';args.protocol=input('协议 chat / responses / messages [chat]：').strip() or 'chat';args.prompt=input('请输入要处理的实际任务（本次调用可能消耗积分）：').strip()
 if args.realm not in ('cn','global') or args.protocol not in ('chat','responses','messages'):p.error('请选择有效的版本与协议。')
 headers={'Authorization':'Bearer '+key,'Content-Type':'application/json'}
 model=args.realm+':fast-model';origin='http://'+cfg['listen']
 if args.models:url=origin+'/v1/models';body=None
 else:
  if not args.prompt or not args.prompt.strip():p.error('请填写 --prompt，或使用 --interactive。')
  if args.protocol=='chat':url=origin+'/v1/chat/completions';body={'model':model,'messages':[{'role':'user','content':args.prompt}],'stream':False,'max_tokens':128}
  elif args.protocol=='responses':url='http://127.0.0.1:8787/v1/responses';body={'model':model,'input':args.prompt,'stream':False,'max_output_tokens':128}
  else:url='http://127.0.0.1:8787/v1/messages';body={'model':model,'messages':[{'role':'user','content':args.prompt}],'stream':False,'max_tokens':128};headers['anthropic-version']='2023-06-01'
 try:
  with urlopen(Request(url,data=json.dumps(body,ensure_ascii=False).encode('utf-8') if body is not None else None,headers=headers),timeout=150) as r:result=json.loads(r.read())
 except HTTPError as e:print(('HTTP '+str(e.code)+': '+e.read().decode('utf-8',errors='replace')).replace(key,'[API Key 已隐藏]'));return 1
 except OSError as e:print(str(e).replace(key,'[API Key 已隐藏]'));return 1
 print(json.dumps(result,ensure_ascii=False,indent=2));return 0
if __name__=='__main__':sys.exit(main())
