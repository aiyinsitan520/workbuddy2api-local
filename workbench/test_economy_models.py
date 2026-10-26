import unittest
from economy_models import economy_candidates,model_unavailable
from test_daily_tasks import DailyTests

class EconomyTests(unittest.TestCase):
    def test_free_then_cheapest_confirmed_rate_with_ceiling(self):
        rows=[{'id':'fast','multiplier':.34},{'id':'expensive','multiplier':3},{'id':'unknown','multiplier':None},{'id':'free','multiplier':0},{'id':'cheap','multiplier':.14},{'id':'expired','multiplier':None,'free_unverified':True}]
        self.assertEqual([c['model'] for c in economy_candidates(rows)],['global:free','global:cheap','global:fast'])
    def test_observed_charge_disproves_free_label(self):
        self.assertEqual(economy_candidates([{'id':'fakefree','multiplier':0,'observed_per_1k':1}]),[])
        self.assertEqual(economy_candidates([{'id':'observedfree','multiplier':None,'observed_per_1k':0}])[0]['multiplier'],0)
    def test_retry_only_clear_model_rejection(self):
        self.assertTrue(model_unavailable(ValueError('model_not_found')))
        for message in ['timeout','500','401','reward not granted']:
            self.assertFalse(model_unavailable(ValueError(message)))

class EconomyDailyTests(DailyTests):
    def economy(self):
        self.d.pricing=lambda uid:[{'id':'free','multiplier':0},{'id':'cheap','multiplier':.14}]
        self.d.configure({'accounts':{'g1':dict(enabled=True,prompt='Summarize the supplied project notes.',model='global:auto-economy',max_tokens=64)}})
    def test_routing_pause_does_not_disable_enabled_daily_task(self):
        self.accounts[0].update(enabled=False,automation_enabled=True)
        self.economy();self.d.tick()
        self.assertEqual(self.calls[0][1]['model'],'global:free')
    def test_one_fallback_after_model_rejected(self):
        self.economy();calls=[]
        def call(uid,body):
            calls.append(body['model'])
            if len(calls)==1:raise ValueError('model_not_found')
            return {'choices':[{'message':{'content':'Real summary'}}]}
        self.d.call=call;self.d.tick();self.d.tick()
        self.assertEqual(calls,['global:free','global:cheap'])
    def test_unknown_outcome_no_paid_retry(self):
        self.economy();calls=[]
        def call(uid,body):calls.append(body['model']);raise ValueError('timeout')
        self.d.call=call;self.d.tick();self.d.tick()
        self.assertEqual(calls,['global:free'])
        self.assertTrue(self.d.journal['accounts']['g1']['paused'])
    def test_missing_pricing_no_inference(self):
        self.economy();self.d.pricing=lambda uid:[];self.d.tick()
        self.assertFalse(self.calls)

if __name__=='__main__':unittest.main()
