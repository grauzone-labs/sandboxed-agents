"""Public procedural guard scripts; no real native program or capture is launched."""
import copy,json,subprocess,tempfile,unittest
from pathlib import Path
ROOT=Path(__file__).resolve().parents[1]
class CaptureGuards(unittest.TestCase):
    def ready(self,mutate=None):
        c=dict(nonce='once',method='binary',pid=10)
        r=dict(schema_version=2,nonce='once',method='binary',item_pid=10,elevated_observer=True,
               wpr=dict(active=True,start_exit=0,collectors_exit=0,profiles_exit=0,stacks=['FileWrite']),
               procmon=dict(pid=50,start_ticks=123,advanced_output=False,drop_filtered_events=False,confirmed_by='human-elevated-observer',activity=['File System','Registry','Process/Thread'],columns=['Date & Time','Thread ID','Duration']))
        if mutate:mutate(r)
        with tempfile.TemporaryDirectory() as d:
            p=Path(d);(p/'r.json').write_text(json.dumps(r));(p/'c.json').write_text(json.dumps(c))
            (p/'run.ps1').write_text("$r=Get-Content -Raw '"+str(p/'r.json')+"'|ConvertFrom-Json\n$c=Get-Content -Raw '"+str(p/'c.json')+"'|ConvertFrom-Json\n& '"+str(ROOT/'Test-InstallerReady.ps1')+"' -Record $r -Context $c\n")
            return subprocess.run(['pwsh','-NoLogo','-NoProfile','-NonInteractive','-File',str(p/'run.ps1')],text=True,capture_output=True,timeout=15)
    def test_real_ready_record_passes(self):self.assertEqual(self.ready().returncode,0)
    def test_string_false_is_not_visual_confirmation(self):
        self.assertNotEqual(self.ready(lambda r:r['procmon'].__setitem__('drop_filtered_events','False')).returncode,0)
    def test_ea_names_are_not_inferred_from_arbitrary_text(self):
        p=subprocess.run(['pwsh','-NoLogo','-NoProfile','-NonInteractive','-File',str(ROOT/'Read-EaNames.ps1'),'-Text','unrecognized output mentions $KERNEL.PURGE.ESBCACHE','-ExitCode','0'],text=True,capture_output=True)
        self.assertEqual(p.returncode,0);self.assertFalse(p.stdout.strip())

def reject(self,change):self.assertNotEqual(self.ready(change).returncode,0)
for name,change in {
    'wrong_nonce':lambda r:r.__setitem__('nonce','other'),
    'wrong_item':lambda r:r.__setitem__('item_pid',11),
    'wpr_inactive':lambda r:r['wpr'].__setitem__('active',False),
    'wpr_start_failed':lambda r:r['wpr'].__setitem__('start_exit',1),
    'wpr_no_stacks':lambda r:r['wpr'].__setitem__('stacks',[]),
    'advanced_output':lambda r:r['procmon'].__setitem__('advanced_output',True),
    'drop_filters':lambda r:r['procmon'].__setitem__('drop_filtered_events',True),
    'missing_activity':lambda r:r['procmon'].__setitem__('activity',['File System']),
    'missing_tid_column':lambda r:r['procmon'].__setitem__('columns',['Date & Time','Duration']),
}.items():
    def case(self,change=change):reject(self,change)
    setattr(CaptureGuards,'test_reject_'+name,case)
if __name__=='__main__':unittest.main()
