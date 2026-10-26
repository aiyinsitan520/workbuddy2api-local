"""Preserve the previous local API protocols while using one Go account pool."""
from pathlib import Path
import json,secrets,sys
import httpx,uvicorn
from fastapi import FastAPI,HTTPException
from fastapi.responses import RedirectResponse

ROOT=Path(__file__).resolve().parent.parent
sys.path.insert(0,str(Path(__file__).resolve().parent))
from core import converter

def cfg(): return json.loads((ROOT/'config.json').read_text(encoding='utf-8-sig'))
def upstream_url(): return 'http://'+cfg()['listen']
def check_auth(authorization,x_api_key):
    key=authorization[7:].strip() if authorization and authorization.startswith('Bearer ') else x_api_key or ''
    keys=[cfg()['api_key']]+json.loads((ROOT/'compat/config.json').read_text(encoding='utf-8'))['legacy_api_keys']
    if not any(secrets.compare_digest(key,k) for k in keys if k): raise HTTPException(401,'invalid api key')

class GatewayCredentials:
    def get_headers(self): return {'Authorization':'Bearer '+cfg()['api_key'],'Content-Type':'application/json'}
    def summary(self): return {'source':'unified-account-pool'}

converter.BACKEND=upstream_url()
converter.CONFIG.update(api_key='managed',cred=GatewayCredentials(),log_path=str(ROOT/'logs/compat.log'),desensitize=False)
converter._check_auth=check_auth
original_models=converter.get_available_models
def models():
    try:
        with httpx.Client(timeout=10) as client:
            r=client.get(upstream_url()+'/v1/models',headers=GatewayCredentials().get_headers());r.raise_for_status()
            return [m['id'] for m in r.json()['data']]
    except Exception: return original_models()
converter.get_available_models=models

app=FastAPI(docs_url=None,redoc_url=None,openapi_url=None)
@app.get('/')
@app.get('/admin')
@app.get('/admin/')
async def dashboard(): return RedirectResponse('http://127.0.0.1:7864/',status_code=307)
@app.get('/health')
async def health():
    async with httpx.AsyncClient(timeout=5) as client:
        r=await client.get(upstream_url()+'/healthz')
        return {'status':'ok' if r.status_code==200 else 'unavailable','gateway':r.json(),'source':'unified-account-pool'}
app.mount('/',converter.app)
if __name__=='__main__': uvicorn.run(app,host='127.0.0.1',port=8787,log_level='warning',proxy_headers=False)
