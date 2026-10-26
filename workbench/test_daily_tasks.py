import tempfile,unittest,sys
from pathlib import Path
from datetime import datetime,timedelta
sys.path.insert(0,str(Path(__file__).parent))
from daily_activity import DailyTasks,TZ

class DailyTests(unittest.TestCase):
 def setUp(self):
  self.temp=tempfile.TemporaryDirectory();self.root=Path(self.temp.name);self.now=datetime(2026,10,9,6,30,tzinfo=TZ);self.calls=[]
  self.accounts=[dict(uid='g1',nickname='G1',realm='global',online=True,enabled=True),dict(uid='g2',nickname='G2',realm='global',online=True,enabled=True),dict(uid='cn',nickname='CN',realm='cn',online=True,enabled=True)]
  self.d=DailyTasks(self.root,lambda:self.accounts,self.call,clock=lambda:self.now,randbelow=lambda n:0)
 def tearDown(self):self.temp.cleanup()
 def call(self,uid,body):self.calls.append((uid,body));return {'choices':[{'message':{'content':'Summary'},'finish_reason':'stop'}]}
 def config(self,uid='g1',enabled=True,prompt='Summarize my supplied project notes.'):
  self.d.configure({'accounts':{uid:dict(enabled=enabled,prompt=prompt,model='global:fast-model',max_tokens=64)}})
 def test_at_most_once_persistent_target(self):
  self.config();self.d.tick();self.d.tick();self.assertEqual([x[0] for x in self.calls],['g1']);self.assertEqual(self.calls[0][1]['max_tokens'],64)
  d=DailyTasks(self.root,lambda:self.accounts,self.call,clock=lambda:self.now,randbelow=lambda n:0);d.tick();self.assertEqual(len(self.calls),1)
 def test_blank_or_cn_rejected(self):
  with self.assertRaises(ValueError):self.config(prompt=' ')
  with self.assertRaises(ValueError):self.config(uid='cn')
 def test_failure_pauses_and_resume_no_duplicate(self):
  self.config();self.d.call=lambda *a:(_ for _ in ()).throw(ValueError('failed'));self.d.tick();self.assertTrue(self.d.view()['accounts'][0]['paused']);self.d.resume('g1');self.d.call=self.call;self.d.tick();self.assertFalse(self.calls)
 def test_missed_not_replayed(self):
  self.now=self.now.replace(hour=5);self.config();self.now=self.now.replace(hour=7);self.d.tick();self.assertFalse(self.calls);self.assertEqual(self.d.journal['accounts']['g1']['days']['2026-10-09']['state'],'missed')
 def test_window_and_disabled(self):
  self.d.tick();self.assertFalse(self.calls);self.now=self.now.replace(hour=5);self.config();self.d.tick();self.assertFalse(self.calls)
 def test_crash_unknown_not_repeated(self):
  self.config();self.d.journal['accounts']['g1']['days']['2026-10-09']['state']='running';self.d.write(self.d.journal_path,self.d.journal)
  d=DailyTasks(self.root,lambda:self.accounts,self.call,clock=lambda:self.now);d.tick();self.assertTrue(d.view()['accounts'][0]['paused']);self.assertFalse(self.calls)

if __name__=='__main__':unittest.main()
