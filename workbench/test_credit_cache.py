import unittest
from credit_cache import merge_credits

class CreditCacheTests(unittest.TestCase):
    def test_failure_retains_last_success_and_other_realms(self):
        cache={'ts':10,'accounts':[{'uid':'cn','ok':True,'remain':90,'used':10,'size':100}, {'uid':'global','ok':True,'remain':12,'used':8,'size':20}]}
        task={'name':'credits','id':'job','state':'running','started_at':20,'done':1,'total':2,'results':[{'uid':'cn','ok':False,'error':'timeout','ts':21}]}
        result=merge_credits(cache,task)
        row=result['accounts'][0]
        self.assertEqual((row['remain'],row['used'],row['ts']),(90,10,10))
        self.assertTrue(row['stale']);self.assertFalse(row['query_ok'])
        self.assertEqual(result['accounts'][1]['remain'],12)
        self.assertNotIn('stale',cache['accounts'][0])
        task.update(state='completed',finished_at=22,done=2,results=[{'uid':'cn','ok':True,'remain':80,'used':20,'size':100,'ts':22}])
        recovered=merge_credits(result,task)['accounts'][0]
        self.assertFalse(recovered['stale']);self.assertNotIn('error',recovered)
        self.assertEqual(recovered['remain'],80)

    def test_failure_without_previous_success_is_unknown_not_zero(self):
        task={'name':'credits','state':'completed','finished_at':22,'results':[{'uid':'new','ok':False,'error':'500'}]}
        result=merge_credits({'accounts':[]},task)
        self.assertNotIn('remain',result['accounts'][0])
        self.assertEqual(merge_credits(result,task),result)

    def test_running_results_update_without_waiting_for_batch(self):
        task={'name':'credits','state':'running','started_at':22,'done':1,'total':2,'results':[{'uid':'first','ok':True,'remain':55,'used':5,'size':60,'ts':23}]}
        result=merge_credits({'accounts':[]},task)
        self.assertEqual(result['accounts'][0]['remain'],55)
        self.assertEqual(result['query']['done'],1)

    def test_older_concurrent_poll_cannot_overwrite_newer_results(self):
        task={'name':'credits','id':'job','state':'completed','started_at':22,'finished_at':25,'done':1,'total':1,'results':[{'uid':'first','ok':True,'remain':55,'used':5,'size':60,'ts':25}]}
        result=merge_credits({'accounts':[]},task)
        old={**task,'state':'running','done':0,'results':[]}
        self.assertEqual(merge_credits(result,old),result)
        old.update(id='older',started_at=20,state='completed')
        self.assertEqual(merge_credits(result,old),result)

if __name__=='__main__':unittest.main()
