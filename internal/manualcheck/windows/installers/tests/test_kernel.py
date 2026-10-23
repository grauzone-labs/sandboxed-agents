"""Exercise the real reviewer/parser with small synthetic export files, not proof mocks."""
import copy, csv, hashlib, json, struct, subprocess, tempfile, unittest
from pathlib import Path
from xml.etree.ElementTree import Element, SubElement, tostring
ROOT = Path(__file__).resolve().parents[1]
CRITERION = 'c'*64
CANDIDATE = 'a5c2c19409a5c2c19409a5c2c19409a5c2c19409'
GUID = dict(header='68fdd900-4a3e-11d1-84f4-0000f80464e3', file='90cbdc39-4a3e-11d1-84f4-0000f80464e3', stack='def2fe46-7bd6-4b80-bd94-f57fe20d0ce3', image='2cb15d1d-5fc1-11d2-abe1-00a0c911f518', process='3d6fa8d0-fe05-11d0-9dda-00c04fd7ba7c', thread='3d6fa8d1-fe05-11d0-9dda-00c04fd7ba7c')
PROC = r'C:\ProgramData\chocolatey\lib\procmon\tools\Procmon64.exe'
BACK = r'C:\traces\synthetic\procmon.pml'
COMMAND = f'"{PROC}" /AcceptEula /Quiet /NoConnect /BackingFile "{BACK}"'
BASE = 0xfffff80010000000

def digest(p): return hashlib.sha256(Path(p).read_bytes()).hexdigest()

def ev(kind, op, qpc, pid=4294967295, tid=4294967295, version=3, fields=None, raw=None):
    e=Element('Event');s=SubElement(e,'System');SubElement(s,'Provider',Guid='{9e814aad-3204-11d2-9a82-006008a86939}')
    SubElement(s,'Version').text=str(version);SubElement(s,'Opcode').text=str(op)
    SubElement(s,'TimeCreated',RawTime=str(qpc));SubElement(s,'Execution',ProcessID=str(pid),ThreadID=str(tid))
    if fields is not None:
        d=SubElement(e,'EventData')
        for k,v in fields.items(): SubElement(d,'Data',Name=k).text=str(v)
    if raw is not None: SubElement(SubElement(e,'ProcessingErrorData'),'EventPayload').text=raw.hex()
    SubElement(SubElement(e,'ExtendedTracingInfo'),'EventGuid').text='{'+GUID.get(kind,kind)+'}'
    return e

def write(qpc=500, pid=10, tid=20, payload_tid=20, offset=4096, length=8192, flags=0x43, version=3):
    return ev('file',68,qpc,pid,tid,version,raw=struct.pack('<QQQQIII',offset,0x11,0x22,0x33,payload_tid,length,flags))

def stack(qpc=500,pid=10,tid=20,frames=(BASE+0x10,),named=False):
    if named:return ev('stack',32,qpc+1,version=2,fields={'EventTimeStamp':qpc,'StackProcess':pid,'StackThread':tid,**{f'Stack{i}':hex(v) for i,v in enumerate(frames,1)}})
    return ev('stack',32,qpc+1,version=2,raw=struct.pack('<QII',qpc,pid,tid)+b''.join(struct.pack('<Q',v) for v in frames))

class Fixture:
    def __init__(self, directory):
        self.root=Path(directory);self.frames=[]
        self.rows=[{'Process Name':'unrelated.exe','PID':'99','TID':'99','Operation':'ReadFile','Path':r'C:\outside','Result':'SUCCESS','Detail':''},
                   {'Process Name':'pwsh.exe','PID':'10','TID':'20','Operation':'WriteFile','Path':r'C:\$LogFile','Result':'SUCCESS','Detail':'Offset: 4.096, Length: 8.192, I/O Flags: Non-cached, Paging I/O, Synchronous Paging I/O'}]
        life=lambda op,q: ev('process',op,q,version=4,fields={'ProcessId':500,'UniqueProcessKey':'0xabc','CommandLine':COMMAND,'ImageFileName':'Procmon64.exe'})
        thread=bytearray(68);struct.pack_into('<II',thread,0,10,20)
        self.events=[ev('header',0,100,version=2,fields={'PointerSize':8,'EventsLost':0,'BuffersLost':0}),
            ev('image',3,120,fields={'ImageBase':hex(BASE),'ImageSize':'0x10000','FileName':r'\SystemRoot\System32\Drivers\Ntfs.sys','ProcessId':0}),
            ev('process',3,130,version=4,fields={'ProcessId':10,'UniqueProcessKey':'0x123'}),
            ev('thread',3,140,raw=thread),life(1,200),
            ev('file',36,300,fields={'FileObject':'0x33','FileName':r'\Device\HarddiskVolume3\$LogFile'}),write(),stack(),life(2,900),
            ev('header',8,1000,fields={})]
        self.capture={'schema_version':2,'method':'binary','criterion_sha256':CRITERION,'captured_commit':CANDIDATE,
            'wpr_start_utc':'2026-10-22T01:00:00Z','wpr_stop_utc':'2026-10-22T01:05:00Z',
            'procmon':{'pid':500,'path':PROC,'backing_file':BACK,'command_line':COMMAND,'start_utc':'2026-10-22T01:01:00Z','end_utc':'2026-10-22T01:04:00Z'},
            'profile_path':str(ROOT/'InstallerJournal.wprp'),'profile_sha256':digest(ROOT/'InstallerJournal.wprp'),
            'settings':{'drop_filtered_events':False,'advanced_output':False,'export_applied_display_filter':False,'human_confirmed':True,'activity':['File System','Registry','Process/Thread']}}
        self.vol={'drive':'C:','nt_device':r'\Device\HarddiskVolume3','filesystem':'NTFS','drive_type':'Fixed','windows_directory':r'C:\Windows','utc':'2026-10-22T00:59:00Z'}
        self.after=copy.deepcopy(self.vol);self.after['utc']='2026-10-22T01:06:00Z'
        self.artifact_change=None;self.pin_change=None
    def save(self):
        r=self.root;csvpath=r/'procmon.csv'
        with csvpath.open('w',newline='') as s:
            w=csv.DictWriter(s,fieldnames=list(self.rows[0]));w.writeheader();w.writerows(self.rows)
        pm=Element('procmon');el=SubElement(pm,'eventlist')
        for i,row in enumerate(self.rows):
            e=SubElement(el,'event')
            for k,v in row.items():SubElement(e,k.replace(' ','_')).text=str(v)
            st=SubElement(e,'stack')
            for path in self.frames if row['Operation']=='WriteFile' else []:SubElement(SubElement(st,'frame'),'path').text=path
        (r/'procmon.xml').write_bytes(tostring(pm))
        et=Element('Events');et.extend(self.events);(r/'kernel.rawtime.xml').write_bytes(tostring(et))
        for f in ['procmon.pml','kernel.etl','kernel.summary.xml']: (r/f).write_bytes(b'synthetic raw file, never acceptance evidence')
        for name,obj in [('capture.json',self.capture),('volume-before.json',self.vol),('volume-after.json',self.after)]: (r/name).write_text(json.dumps(obj))
        pins={'schema_version':2,'created_utc':'2026-10-22T00:58:00Z','files':[{'path':str(p),'sha256':digest(p)} for p in sorted(ROOT.rglob('*')) if p.is_file() and p.suffix in ('.ps1','.cs','.py','.wprp')]}
        # Synthetic immutable tool artifacts exercise public hash provenance; none is executable or run.
        for native in ('Procmon64.exe','wpr.exe','tracerpt.exe'):
            tool=r/native;tool.write_bytes(('synthetic pinned tool '+native).encode())
            pins['files'].append({'path':str(tool),'sha256':digest(tool)})
        if self.pin_change:self.pin_change(pins)
        (r/'pins.json').write_text(json.dumps(pins))
        roles={'pml':'procmon.pml','procmon_xml':'procmon.xml','etl':'kernel.etl','kernel_xml':'kernel.rawtime.xml','kernel_summary':'kernel.summary.xml','capture':'capture.json','volume_before':'volume-before.json','volume_after':'volume-after.json','pins':'pins.json'}
        m={'schema_version':2,'artifacts':{k:{'path':str(r/v),'sha256':digest(r/v)} for k,v in roles.items()}}
        if self.artifact_change:self.artifact_change(m)
        (r/'evidence.json').write_text(json.dumps(m))
        Path(str(csvpath)+'.export.json').write_text(json.dumps({'csvSha256':digest(csvpath),'sourcePmlSha256':digest(r/'procmon.pml'),'exportAppliedDisplayFilter':False}))
        return csvpath

class KernelTests(unittest.TestCase):
    def run_fixture(self, change=None):
        d=tempfile.TemporaryDirectory();self.addCleanup(d.cleanup);f=Fixture(d.name)
        if change:change(f)
        csv=f.save()
        p=subprocess.run(['pwsh','-NoLogo','-NoProfile','-NonInteractive','-File',str(ROOT/'Review-InstallerTrace.ps1'),'-Csv',str(csv),'-ProcessIds','10','-InstallRoot',r'C:\Users\agent\chk-install','-BookkeepingCriterion','windows-bookkeeping-v1','-CriterionSha256',CRITERION,'-OutputDirectory',str(f.root/'review'),'-KernelTraceEvidence',str(f.root/'evidence.json'),'-Candidate',CANDIDATE],text=True,capture_output=True,timeout=40)
        out=f.root/'review'/'procmon.csv.summary.json'
        return p,json.loads(out.read_text()) if out.exists() else None,f
    def test_frameless_journal_uses_whole_thread_etw_stack_without_cross_tool_time(self):
        p,s,f=self.run_fixture()
        self.assertEqual(p.returncode,0,p.stdout+p.stderr)
        self.assertEqual(s['bookkeeping_rows'],1)
        self.assertEqual(s['verdict'],'NO_FINDINGS')
        self.assertEqual(s['kernel_trace']['accepted_full_ordinals'],[2])
        self.assertEqual(s['kernel_trace']['groups'][0]['events'][0]['ntfs_frames'],['0xFFFFF80010000010'])


    def test_csv_crlf_and_xml_normalized_newlines_bind_the_same_event(self):
        p,s,_=self.run_fixture(lambda f:f.rows[0].__setitem__('Detail','first line\r\nsecond line'))
        self.assertEqual(p.returncode,0,p.stdout+p.stderr)
        self.assertEqual(s['bookkeeping_rows'],1)

    def test_process_environment_separator_representation_preserves_embedded_newlines(self):
        p,s,_=self.run_fixture(environment_separators)
        self.assertEqual(p.returncode,0,p.stdout+p.stderr)
        self.assertEqual(s['bookkeeping_rows'],1)

    def test_registry_string_display_delimiters_keep_other_fields_exact(self):
        for kind in ('REG_SZ','REG_EXPAND_SZ','REG_MULTI_SZ'):
            with self.subTest(kind=kind):
                p,s,_=self.run_fixture(lambda f:registry_separators(f,kind=kind))
                self.assertEqual(p.returncode,0,p.stdout+p.stderr)
                self.assertEqual(s['bookkeeping_rows'],1)

    def test_sync_paging_without_independent_paging_flag_never_qualifies(self):
        def change(f):
            f.rows[1]['Detail']='Offset: 4.096, Length: 8.192, I/O Flags: Non-cached, Synchronous Paging I/O'
            f.events[6]=write(flags=0x41)
        p,s,_=self.run_fixture(change)
        self.assertEqual(p.returncode,0,p.stdout+p.stderr)
        self.assertEqual(s['bookkeeping_rows'],0)
        self.assertNotEqual(s['verdict'],'NO_FINDINGS')
    def test_optional_sync_paging_flag_can_be_absent_in_both_tools(self):
        def change(f):
            f.rows[1]['Detail']='Offset: 4.096, Length: 8.192, I/O Flags: Non-cached, Paging I/O'
            f.events[6]=write(flags=3)
        p,s,_=self.run_fixture(change)
        self.assertEqual(p.returncode,0,p.stdout+p.stderr)
        self.assertEqual(s['bookkeeping_rows'],1)

    def test_read_only_registry_path_accounts_for_xml_surrogate_rendering(self):
        p,s,_=self.run_fixture(registry_surrogate_path)
        self.assertEqual(p.returncode,0,p.stdout+p.stderr)
        self.assertEqual(s['bookkeeping_rows'],1)

    def test_nonproof_etw_metadata_can_contain_repeated_array_fields(self):
        p,s,_=self.run_fixture(lambda f:f.events.append(metadata_array_event(600)))
        self.assertEqual(p.returncode,0,p.stdout+p.stderr)
        self.assertEqual(s['bookkeeping_rows'],1)

    def test_stack_reference_payload_is_not_an_event_execution_thread(self):
        def change(f):f.events[7].find('./System/TimeCreated').set('RawTime','500')
        p,s,_=self.run_fixture(change)
        self.assertEqual(p.returncode,0,p.stdout+p.stderr)
        self.assertEqual(s['bookkeeping_rows'],1)

    def test_exact_numeric_image_metadata_does_not_claim_a_module_range(self):
        def change(f):
            f.events.extend([ev('image',33,600,version=2,fields={'ImageBase':hex(BASE)}),ev('image',34,601,version=2,fields={'HypercallPageVa':hex(BASE)})])
        p,s,_=self.run_fixture(change)
        self.assertEqual(p.returncode,0,p.stdout+p.stderr)
        self.assertEqual(s['bookkeeping_rows'],1)
        self.assertEqual(s['kernel_trace']['ntfs_range']['image_size'],65536)

    def test_preexisting_instances_can_be_proved_by_unique_stop_rundown(self):
        p,s,_=self.run_fixture(lambda f:preexisting_lifetimes(f,4))
        self.assertEqual(p.returncode,0,p.stdout+p.stderr)
        self.assertEqual(s['bookkeeping_rows'],1)

    def test_preexisting_instances_can_end_once_during_the_trace(self):
        p,s,_=self.run_fixture(lambda f:preexisting_lifetimes(f,2))
        self.assertEqual(p.returncode,0,p.stdout+p.stderr)
        self.assertEqual(s['bookkeeping_rows'],1)

    def test_documented_v2_file_name_events_join_the_exact_key(self):
        p,s,_=self.run_fixture(lambda f:f.events[5].find('./System/Version').__setattr__('text','2'))
        self.assertEqual(p.returncode,0,p.stdout+p.stderr)
        self.assertEqual(s['bookkeeping_rows'],1)

    def test_nonempty_saved_stack_without_ntfs_never_overrides(self):
        p,s,_=self.run_fixture(lambda f:setattr(f,'frames',[r'C:\arbitrary\other.sys']))
        self.assertEqual(p.returncode,0,p.stdout+p.stderr)
        self.assertEqual(s['bookkeeping_rows'],0)
        self.assertEqual(s['verdict'],'INCOMPLETE')
    def test_native_saved_ntfs_stack_is_judged_alone_when_etw_fails(self):
        def change(f):
            f.frames=[r'C:\Windows\System32\drivers\Ntfs.sys']
            field(f.events[0],'BuffersLost',1)
        p,s,_=self.run_fixture(change)
        self.assertEqual(p.returncode,0,p.stdout+p.stderr)
        self.assertEqual(s['bookkeeping_rows'],1)
    def test_saved_ntfs_stack_is_independent_of_a_bad_etw_profile_pin(self):
        def change(f):
            f.frames=[r'C:\Windows\System32\drivers\Ntfs.sys']
            f.capture['profile_sha256']='a'*64
        p,s,_=self.run_fixture(change)
        self.assertEqual(p.returncode,0,p.stdout+p.stderr)
        self.assertEqual(s['bookkeeping_rows'],1)

    def test_unset_header_pid_write_is_included_using_unique_thread_owner(self):
        p,s,_=self.run_fixture(lambda f:f.events.__setitem__(6,write(pid=4294967295)))
        self.assertEqual(p.returncode,0,p.stdout+p.stderr);self.assertEqual(s['bookkeeping_rows'],1)
    def test_named_stack_representation_runs_real_parser(self):
        p,s,_=self.run_fixture(lambda f:f.events.__setitem__(7,stack(named=True)))
        self.assertEqual(p.returncode,0,p.stdout+p.stderr);self.assertEqual(s['bookkeeping_rows'],1)
    def test_extra_trace_writes_are_allowed_only_with_ntfs_stacks(self):
        p,s,_=self.run_fixture(lambda f:f.events.extend([write(50,offset=123),stack(50)]))
        self.assertEqual(p.returncode,0,p.stdout+p.stderr);self.assertEqual(s['bookkeeping_rows'],1)
        self.assertEqual(len(s['kernel_trace']['groups'][0]['events']),2)
    def test_full_thread_lifetime_requires_equal_counts_and_real_end_events(self):
        p,s,_=self.run_fixture(full_lifetime)
        self.assertEqual(p.returncode,0,p.stdout+p.stderr);self.assertEqual(s['bookkeeping_rows'],1)
        self.assertTrue(s['kernel_trace']['groups'][0]['full_thread_lifetime'])
    def test_unknown_npm_pipe_operation_stays_incomplete(self):
        def change(f):f.rows.append({**f.rows[1],'Operation':'<Unknown>','Path':r'\Device\NamedPipe\unknown','Detail':''})
        p,s,_=self.run_fixture(change)
        self.assertEqual(p.returncode,0,p.stdout+p.stderr);self.assertEqual(s['bookkeeping_rows'],1)
        self.assertEqual(s['unknown_rows'],1);self.assertEqual(s['verdict'],'INCOMPLETE')

# Each mutation crosses the public PowerShell seam and runs all real parsers.
def rejected(self, change):
    p,s,_=self.run_fixture(change)
    self.assertEqual(p.returncode,0,p.stdout+p.stderr)
    self.assertEqual(s['bookkeeping_rows'],0)
    self.assertFalse(s['kernel_trace']['complete'])
    self.assertTrue(s['kernel_trace']['failure'])
    self.assertNotEqual(s['verdict'],'NO_FINDINGS')

def field(e,name,value):
    for x in e.findall('./EventData/Data'):
        if x.attrib['Name']==name:x.text=str(value);return
    SubElement(e.find('EventData'),'Data',Name=name).text=str(value)

def preexisting_lifetimes(f,opcode):
    for index,time in ((2,800),(3,801)):
        f.events[index].find('./System/Opcode').text=str(opcode)
        f.events[index].find('./System/TimeCreated').set('RawTime',str(time))

def stop_rundown_duplicate(f):
    preexisting_lifetimes(f,4);f.events.append(copy.deepcopy(f.events[3]))

def ended_then_restarted(f):
    preexisting_lifetimes(f,2)
    thread=bytearray(68);struct.pack_into('<II',thread,0,10,20)
    f.events.append(ev('thread',1,850,raw=thread))

def extra_bad_write(f):f.events.extend([write(50,offset=123),stack(50,frames=(0x777,))])
def unknown_collision(f):f.events.append(ev('file',67,500,pid=4294967295,tid=20,fields={}))
def full_lifetime(f):
    template=f.rows[1].copy();a={**template,'Operation':'Thread Create','Detail':'Thread ID: 20','Path':r'C:\thread'};b={**a,'Operation':'Thread Exit'}
    f.rows.insert(1,a);f.rows.append(b)
    thread=bytearray(68);struct.pack_into('<II',thread,0,10,20)
    f.events[3]=ev('thread',1,140,raw=thread);f.events.append(ev('thread',2,800,raw=thread))

def extra_full(f):full_lifetime(f);f.events.extend([write(600),stack(600)])
def extra_fingerprint(f):full_lifetime(f);f.events.extend([write(600,offset=999),stack(600)])
def changed_xml(f):
    original=f.save
    def save():
        p=original();r=f.root/'procmon.xml';r.write_text(r.read_text().replace('<TID>20</TID>','<TID>21</TID>'));m=json.loads((f.root/'evidence.json').read_text());m['artifacts']['procmon_xml']['sha256']=digest(r);(f.root/'evidence.json').write_text(json.dumps(m));return p
    f.save=save

def duplicate_xml(f):
    original=f.save
    def save():
        p=original();r=f.root/'procmon.xml';r.write_text(r.read_text().replace('</eventlist>','<event><PID>99</PID></event></eventlist>'));m=json.loads((f.root/'evidence.json').read_text());m['artifacts']['procmon_xml']['sha256']=digest(r);(f.root/'evidence.json').write_text(json.dumps(m));return p
    f.save=save

def time_mismatch(f):
    for r in f.rows:r['Time of Day']='01:02:03.000'
    original=f.save
    def save():
        p=original();r=f.root/'procmon.xml';r.write_text(r.read_text().replace('01:02:03.000','01:02:03.001'));m=json.loads((f.root/'evidence.json').read_text());m['artifacts']['procmon_xml']['sha256']=digest(r);(f.root/'evidence.json').write_text(json.dumps(m));return p
    f.save=save

def environment_separators(f,changed_value=False,wrong_operation=False):
    f.rows[0]['Operation']='ReadFile' if wrong_operation else 'Process Start'
    f.rows[0]['Detail']='Parent PID: 1, Command line: first\nsecond, Environment: ONE=first\nsecond;TWO=2'
    original=f.save
    def save():
        p=original();r=f.root/'procmon.xml';text=r.read_text().replace('second;TWO=2','second\nTWO='+('9' if changed_value else '2'));r.write_text(text)
        m=json.loads((f.root/'evidence.json').read_text());m['artifacts']['procmon_xml']['sha256']=digest(r);(f.root/'evidence.json').write_text(json.dumps(m));return p
    f.save=save

def journal_delimiter_difference(f):
    f.rows[1]['Detail']+=', Environment: ONE=1;TWO=2'
    original=f.save
    def save():
        p=original();r=f.root/'procmon.xml';r.write_text(r.read_text().replace('ONE=1;TWO=2','ONE=1\nTWO=2'));m=json.loads((f.root/'evidence.json').read_text());m['artifacts']['procmon_xml']['sha256']=digest(r);(f.root/'evidence.json').write_text(json.dumps(m));return p
    f.save=save

def command_line_delimiter_difference(f):
    environment_separators(f);original=f.save
    def save():
        p=original();r=f.root/'procmon.xml';r.write_text(r.read_text().replace('Command line: first\nsecond','Command line: first;second'));m=json.loads((f.root/'evidence.json').read_text());m['artifacts']['procmon_xml']['sha256']=digest(r);(f.root/'evidence.json').write_text(json.dumps(m));return p
    f.save=save

def registry_separators(f,kind='REG_SZ',changed_value=False,changed_length=False):
    f.rows[0]['Operation']='RegSetValue'
    f.rows[0]['Detail']=f'Type: {kind}, Length: 26, Data: first;second'
    original=f.save
    def save():
        p=original();r=f.root/'procmon.xml';text=r.read_text().replace('Data: first;second','Data: first\n'+('serond' if changed_value else 'second'))
        if changed_length:text=text.replace('Length: 26','Length: 28')
        r.write_text(text);m=json.loads((f.root/'evidence.json').read_text());m['artifacts']['procmon_xml']['sha256']=digest(r);(f.root/'evidence.json').write_text(json.dumps(m));return p
    f.save=save

def registry_surrogate_path(f,operation='RegOpenKey',ordinary_change=False):
    f.rows[0]['Operation']=operation
    f.rows[0]['Path']='HKLM\\Software\\Example\\'+('A' if ordinary_change else chr(0x1f600))
    original=f.save
    def save():
        from xml.etree.ElementTree import fromstring
        p=original();r=f.root/'procmon.xml';doc=fromstring(r.read_bytes());node=doc.find('./eventlist/event/Path');node.text=node.text.replace('A' if ordinary_change else chr(0x1f600),'_' if ordinary_change else '__');r.write_bytes(tostring(doc))
        m=json.loads((f.root/'evidence.json').read_text());m['artifacts']['procmon_xml']['sha256']=digest(r);(f.root/'evidence.json').write_text(json.dumps(m));return p
    f.save=save

def metadata_array_event(qpc):
    e=ev('header',82,qpc,pid=4294967295,tid=20,version=2,fields={'TimeStamp':0})
    SubElement(e.find('EventData'),'Data',Name='TimeStamp').text='0'
    return e

NEGATIVES={
 'native_tool_bytes_changed':lambda f:setattr(f,'pin_change',lambda p:[x.__setitem__('sha256','a'*64) for x in p['files'] if x['path'].endswith('tracerpt.exe')]),

 'native_procmon_pin_missing':lambda f:setattr(f,'pin_change',lambda p:p.__setitem__('files',[x for x in p['files'] if not x['path'].endswith('Procmon64.exe')])),
 'native_wpr_pin_missing':lambda f:setattr(f,'pin_change',lambda p:p.__setitem__('files',[x for x in p['files'] if not x['path'].endswith('wpr.exe')])),
 'native_tracerpt_pin_missing':lambda f:setattr(f,'pin_change',lambda p:p.__setitem__('files',[x for x in p['files'] if not x['path'].endswith('tracerpt.exe')])),

 'stack_execution_header_same_thread_qpc_collision':lambda f:(f.events[7].find('./System/TimeCreated').set('RawTime','500'),f.events[7].find('./System/Execution').set('ThreadID','20')),

 'duplicate_stop_rundown_instance':stop_rundown_duplicate,
 'end_then_new_start_is_thread_reuse':ended_then_restarted,
 'end_plus_stop_rundown_implies_reuse':lambda f:(preexisting_lifetimes(f,2),f.events.append(ev('thread',4,950,fields={'ProcessId':10,'TThreadId':20}))),
 'stop_rundown_thread_association_changes':lambda f:(preexisting_lifetimes(f,4),f.events.append(ev('thread',4,950,fields={'ProcessId':99,'TThreadId':20}))),

 'image_metadata_collision_still_counts':lambda f:f.events.append(ev('image',33,500,tid=20,version=2,fields={'ImageBase':hex(BASE)})),
 'image_metadata_unknown_version':lambda f:f.events.append(ev('image',33,600,version=4,fields={'ImageBase':hex(BASE)})),
 'image_metadata_extra_range_cannot_be_ignored':lambda f:f.events.append(ev('image',33,600,version=2,fields={'ImageBase':hex(BASE),'ImageSize':100})),
 'image_unknown_mapping_opcode':lambda f:f.events[1].find('./System/Opcode').__setattr__('text','99'),

 'file_name_unknown_version':lambda f:f.events[5].find('./System/Version').__setattr__('text','4'),
 'file_name_v2_mismatched_key':lambda f:(f.events[5].find('./System/Version').__setattr__('text','2'),field(f.events[5],'FileObject','0x34')),
 'ignored_array_metadata_still_counts_for_thread_qpc_collision':lambda f:f.events.append(metadata_array_event(500)),
 'loss_header_duplicate_field_remains_rejected':lambda f:SubElement(f.events[0].find('EventData'),'Data',Name='PointerSize').__setattr__('text','8'),
 'ordinary_registry_key_name_change':lambda f:registry_surrogate_path(f,ordinary_change=True),
 'write_path_cannot_use_lossy_xml_surrogates':lambda f:registry_surrogate_path(f,operation='WriteFile'),
 'ea_path_cannot_use_lossy_xml_surrogates':lambda f:registry_surrogate_path(f,operation='SetEAFile'),
 'registry_mutation_cannot_use_lossy_xml_surrogates':lambda f:registry_surrogate_path(f,operation='RegSetValue'),
 'sync_paging_phrase_does_not_supply_paging_flag':lambda f:f.rows[1].__setitem__('Detail','Offset: 4.096, Length: 8.192, I/O Flags: Non-cached, Synchronous Paging I/O'),
 'registry_actual_string_value_change':lambda f:registry_separators(f,changed_value=True),
 'registry_length_change':lambda f:registry_separators(f,changed_length=True),
 'registry_binary_cannot_use_string_delimiters':lambda f:registry_separators(f,kind='REG_BINARY'),
 'journal_detail_cannot_use_environment_delimiter_mapping':journal_delimiter_difference,
 'command_text_cannot_use_environment_delimiter_mapping':command_line_delimiter_difference,
 'environment_actual_value_change':lambda f:environment_separators(f,changed_value=True),
 'delimiter_representation_not_process_start':lambda f:environment_separators(f,wrong_operation=True),
 'csv_xml_time_column_ordinal_mismatch':time_mismatch,
 'thread_start_plus_rundown_not_unique':lambda f:f.events.append(ev('thread',1,300,fields={'ProcessId':10,'TThreadId':20})),
 'recording_profile_not_the_pinned_profile':lambda f:f.capture.__setitem__('profile_sha256','a'*64),
 'full_thread_missing_etw_end':lambda f:(full_lifetime(f),f.events.pop()),
 'attributed_direct_journal_open':lambda f:f.rows.append({**f.rows[1],'Operation':'CreateFile','Detail':'OpenResult: Opened'}),
 'named_stack_field_gap':lambda f:f.events.__setitem__(7,ev('stack',32,501,version=2,fields={'EventTimeStamp':500,'StackProcess':10,'StackThread':20,'Stack2':hex(BASE+16)})),
 'named_stack_duplicate_field':lambda f:(f.events.__setitem__(7,stack(named=True)),SubElement(f.events[7].find('EventData'),'Data',Name='Stack1').__setattr__('text',hex(BASE+16))),
 'qpc_above_double_precision_still_exact':lambda f:(f.events.__setitem__(6,write(qpc=9007199254740993)),f.events.__setitem__(7,stack(qpc=9007199254740992))),
 'entire_trace_not_matching_fingerprint_subset':extra_bad_write,
 'unset_header_pid_event_collision':unknown_collision,
 'other_pid_same_thread_collision':lambda f:f.events.append(ev('file',67,500,pid=99,tid=20,fields={})),
 'same_timestamp_two_writes':lambda f:f.events.append(write()),
 'write_tid_header_payload_disagree':lambda f:f.events.__setitem__(6,write(payload_tid=21)),
 'write_header_tid_unset':lambda f:f.events.__setitem__(6,write(tid=4294967295)),
 'write_header_pid_other':lambda f:f.events.__setitem__(6,write(pid=99)),
 'stack_wrong_original_qpc':lambda f:f.events.__setitem__(7,stack(qpc=499)),
 'stack_wrong_payload_pid':lambda f:f.events.__setitem__(7,stack(pid=99)),
 'stack_wrong_payload_tid':lambda f:f.events.__setitem__(7,stack(tid=21)),
 'stack_duplicate':lambda f:f.events.append(stack()),
 'stack_empty':lambda f:f.events.__setitem__(7,stack(frames=())),
 'stack_other_module':lambda f:f.events.__setitem__(7,stack(frames=(BASE-1,))),
 'stack_at_end_exclusive':lambda f:f.events.__setitem__(7,stack(frames=(BASE+0x10000,))),
 'stack_unknown_version':lambda f:f.events[7].find('./System/Version').__setattr__('text','3'),
 'write_unknown_version':lambda f:f.events.__setitem__(6,write(version=4)),
 'file_key_multiple_names':lambda f:f.events.append(ev('file',36,310,fields={'FileObject':'0x33','FileName':r'\Device\HarddiskVolume3\other'})),
 'two_keys_same_journal_name':lambda f:f.events.append(ev('file',36,310,fields={'FileObject':'0x34','FileName':r'\Device\HarddiskVolume3\$LogFile'})),
 'file_key_wrong_volume':lambda f:field(f.events[5],'FileName',r'\Device\HarddiskVolume4\$LogFile'),
 'ntfs_load_even_same_mapping':lambda f:f.events[1].find('./System/Opcode').__setattr__('text','10'),
 'ntfs_unload':lambda f:f.events[1].find('./System/Opcode').__setattr__('text','2'),
 'ntfs_noncanonical_path':lambda f:field(f.events[1],'FileName',r'C:\evil\Ntfs.sys'),
 'ntfs_mapping_changed':lambda f:f.events.append(ev('image',4,950,fields={'ImageBase':hex(BASE+1),'ImageSize':'0x10000','FileName':r'\SystemRoot\System32\Drivers\Ntfs.sys','ProcessId':0})),
 'ntfs_overlapping_image':lambda f:f.events.append(ev('image',3,110,fields={'ImageBase':hex(BASE-1),'ImageSize':'0x10000','FileName':r'\SystemRoot\System32\Drivers\other.sys','ProcessId':4})),
 'thread_association_wrong':lambda f:f.events.__setitem__(3,ev('thread',3,140,fields={'ProcessId':99,'TThreadId':20})),
 'thread_missing':lambda f:f.events.pop(3),
 'thread_reused':lambda f:f.events.append(copy.deepcopy(f.events[3])),
 'process_reused':lambda f:f.events.append(ev('process',1,800,version=4,fields={'ProcessId':10,'UniqueProcessKey':'0x456'})),
 'process_key_changed':lambda f:f.events.append(ev('process',4,800,version=4,fields={'ProcessId':10,'UniqueProcessKey':'0x456'})),
 'process_missing':lambda f:f.events.pop(2),
 'fingerprint_offset_mismatch':lambda f:f.events.__setitem__(6,write(offset=4097)),
 'fingerprint_length_mismatch':lambda f:f.events.__setitem__(6,write(length=8193)),
 'fingerprint_noncached_mismatch':lambda f:f.events.__setitem__(6,write(flags=0x42)),
 'fingerprint_paging_mismatch':lambda f:f.events.__setitem__(6,write(flags=0x41)),
 'fingerprint_sync_paging_mismatch':lambda f:f.events.__setitem__(6,write(flags=3)),
 'fingerprint_etw_count_short':lambda f:f.rows.append(copy.deepcopy(f.rows[1])),
 'full_thread_counts_not_equal':extra_full,
 'full_thread_extra_fingerprint':extra_fingerprint,
 'etw_started_after_procmon':lambda f:f.capture.__setitem__('wpr_start_utc','2026-10-22T01:02:00Z'),
 'etw_stopped_before_procmon':lambda f:f.capture.__setitem__('wpr_stop_utc','2026-10-22T01:03:00Z'),
 'etw_procmon_start_missing':lambda f:f.events.pop(4),
 'etw_procmon_end_missing':lambda f:f.events.pop(8),
 'etw_procmon_command_backing_mismatch':lambda f:field(f.events[4],'CommandLine',COMMAND.replace('synthetic','wrong')),
 'etw_events_lost':lambda f:field(f.events[0],'EventsLost',1),
 'etw_buffers_lost':lambda f:field(f.events[0],'BuffersLost',1),
 'etw_buffers_lost_not_reported':lambda f:f.events[0].find('EventData').remove(f.events[0].findall('./EventData/Data')[2]),
 'etw_pointer_size_unproven':lambda f:field(f.events[0],'PointerSize',4),
 'drop_filtered_events_on':lambda f:f.capture['settings'].__setitem__('drop_filtered_events',True),
 'advanced_output_on':lambda f:f.capture['settings'].__setitem__('advanced_output',True),
 'display_filter_export':lambda f:f.capture['settings'].__setitem__('export_applied_display_filter',True),
 'readiness_missing':lambda f:f.capture['settings'].__setitem__('human_confirmed',False),
 'profile_tools_pinned_after_capture':lambda f:setattr(f,'pin_change',lambda p:p.__setitem__('created_utc','2026-10-22T02:00:00Z')),
 'test_hash_not_pinned':lambda f:setattr(f,'pin_change',lambda p:p.__setitem__('files',[x for x in p['files'] if not x['path'].endswith('test_kernel.py')])),
 'artifact_hash_changed':lambda f:setattr(f,'artifact_change',lambda m:m['artifacts']['etl'].__setitem__('sha256','a'*64)),
 'volume_mapping_changed':lambda f:f.after.__setitem__('nt_device',r'\Device\HarddiskVolume4'),
 'volume_is_network':lambda f:f.vol.__setitem__('drive_type','Network'),
 'volume_is_not_ntfs':lambda f:f.vol.__setitem__('filesystem','ReFS'),
 'volume_query_before_missing':lambda f:f.vol.__setitem__('utc','2026-10-22T01:03:00Z'),
 'volume_query_after_too_early':lambda f:f.after.__setitem__('utc','2026-10-22T01:03:00Z'),
 'csv_xml_ordinal_mismatch':changed_xml,
 'csv_xml_count_mismatch':duplicate_xml,
}
for name,change in NEGATIVES.items():
    def case(self,change=change):rejected(self,change)
    case.__doc__='Reject '+name.replace('_',' ')+', through Review-InstallerTrace.ps1.'
    setattr(KernelTests,'test_reject_'+name,case)



class StackDepthTests(unittest.TestCase):
    """Raw StackWalk payloads carry 16 header bytes and 8-byte frames. Windows 11 delivers up to 256
    frames (the npm capture of 2026-10-22 held 335 raw stacks of 193 to 256 frames, the deepest
    exactly 16 + 256 x 8 bytes); the reader must decode them and still reject a malformed payload."""
    def run_with_stack_frames(self, frame_count, extra_bytes=0):
        def change(f):
            frames = tuple(BASE + 0x10 + 8 * i for i in range(frame_count))
            event = stack(frames=frames)
            if extra_bytes:
                payload = event.find('.//EventPayload')
                payload.text = payload.text + '00' * extra_bytes
            f.events[7] = event
        return KernelTests.run_fixture(self, change)

    def test_256_frame_raw_stack_decodes(self):
        p, s, _ = self.run_with_stack_frames(256)
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertTrue(s['kernel_trace']['complete'], s['kernel_trace']['failure'])
        self.assertEqual(s['kernel_trace']['groups'][0]['events'][0]['frame_count'], 256)

    def test_257_frame_raw_stack_is_rejected(self):
        p, s, _ = self.run_with_stack_frames(257)
        self.assertFalse(s['kernel_trace']['complete'])
        self.assertIn('StackWalk', s['kernel_trace']['failure'])

    def test_payload_not_a_whole_number_of_frames_is_rejected(self):
        p, s, _ = self.run_with_stack_frames(3, extra_bytes=4)
        self.assertFalse(s['kernel_trace']['complete'])
        self.assertIn('StackWalk', s['kernel_trace']['failure'])


if __name__=='__main__':unittest.main()
