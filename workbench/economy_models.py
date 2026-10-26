"""Select real-task models using fresh account pricing; never chase reward eligibility."""
import math

def economy_candidates(rows, ceiling=0.34):
    candidates=[]
    for row in rows:
        observed=row.get('observed_per_1k')
        rate=row.get('multiplier')
        # An observed charge disproves a zero advertised price for this account.
        free=(isinstance(observed,(int,float)) and observed==0) or (rate==0 and not row.get('free_unverified') and observed is None)
        if free:rank=(0,0)
        elif isinstance(rate,(int,float)) and math.isfinite(rate) and 0<rate<=ceiling:rank=(1,rate)
        else:continue
        efforts=row.get('supported_efforts') or []
        effort='low' if 'low' in efforts or not efforts else efforts[0]
        candidates.append((rank,row['id'],{'model':'global:'+row['id'],'reasoning_effort':effort,'multiplier':0 if free else rate}))
    candidates.sort(key=lambda item:(item[0],item[1]))
    return [item[2] for item in candidates]

def model_unavailable(error):
    # Fallback only after a clear model rejection, never after timeout/unknown
    # execution, account authentication failure, or missing reward deposit.
    text=str(error).lower()
    return any(word in text for word in ('model_not_found','unsupported_model','unknown model','model is not available','模型不可用','模型不支持'))
