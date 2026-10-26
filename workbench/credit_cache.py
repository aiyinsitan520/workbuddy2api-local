"""Merge completed account results without discarding last successful balances."""
from copy import deepcopy

def merge_credits(cache, task):
    out = deepcopy(cache)
    if task.get('name') != 'credits':
        return out
    previous_query = out.get('query', {})
    if task.get('started_at', 0) < (previous_query.get('started_at') or 0):
        return out
    if task.get('id') and task.get('id') == previous_query.get('id'):
        if (task.get('done') or 0) < (previous_query.get('done') or 0):
            return out
        if previous_query.get('state') in ('completed', 'failed') and task.get('state') == 'running':
            return out
    marker = [task.get('id', task.get('started_at')), task.get('state'),
              len(task.get('results') or []), task.get('finished_at')]
    if out.get('marker') == marker:
        return out
    rows = {a['uid']: a for a in out.get('accounts', [])}
    for result in task.get('results') or []:
        item = dict(result)
        timestamp = item.get('ts') or task.get('finished_at') or task.get('started_at')
        if item.get('ok'):
            item.update(ts=timestamp, attempted_at=timestamp, stale=False, query_ok=True)
        elif rows.get(item['uid'], {}).get('ok'):
            previous = rows[item['uid']]
            item = {**previous, 'error': item.get('error', '查询失败'),
                    'ts': previous.get('ts') or out.get('ts'),
                    'attempted_at': timestamp, 'stale': True, 'query_ok': False}
        else:
            item.update(attempted_at=timestamp, query_ok=False)
        rows[item['uid']] = item
    out.update(accounts=list(rows.values()), marker=marker,
               ts=task.get('finished_at') or out.get('ts'),
               query={k: task.get(k) for k in ('id','state','done','total','started_at','finished_at')})
    return out
