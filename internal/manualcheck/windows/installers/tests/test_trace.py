import csv
import hashlib
import json
import subprocess
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parents[1] / 'Review-InstallerTrace.ps1'


class TraceTests(unittest.TestCase):
    def run_trace(self, rows, *options, full_export=False, metadata=None, culture=None):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        trace = Path(directory.name) / 'trace.csv'
        with trace.open('w', newline='') as stream:
            writer = csv.DictWriter(stream, fieldnames=['Process Name', 'PID', 'Operation', 'Path', 'Result', 'Detail'])
            writer.writeheader()
            writer.writerows(rows)
        if full_export:
            Path(str(trace) + '.export.json').write_text(json.dumps({'exportAppliedDisplayFilter': False, 'csvSha256': hashlib.sha256(trace.read_bytes()).hexdigest(), 'sourcePmlSha256': '0' * 64}))
        if metadata is not None:
            stack=Path(directory.name)/'stacks.json';stack.write_text(json.dumps({'exported_events':len(rows),'events':metadata['events']}))
            ea=Path(directory.name)/'ea.json';ea.write_text(json.dumps(metadata['ea']))
            bundle={'schema_version':1,'csv_sha256':hashlib.sha256(trace.read_bytes()).hexdigest(),'source_pml_sha256':'0'*64,'source_xml_sha256':'1'*64,'ntfs_volume_roots':metadata.get('ntfs_volume_roots',[]),'windows_directory':r'C:\Windows','ea_query_root':r'C:\Program Files\PowerShell\7','stack_report_file':str(stack),'stack_report_sha256':hashlib.sha256(stack.read_bytes()).hexdigest(),'ea_query_file':str(ea),'ea_query_sha256':hashlib.sha256(ea.read_bytes()).hexdigest()}
            evidence=Path(directory.name)/'evidence.json';evidence.write_text(json.dumps(bundle))
            options=(*options,'-BookkeepingEvidence',str(evidence))
        scope = [] if '-ShellPid' in options else ['-ProcessIds', '10']
        if '-BookkeepingCriterion' in options:
            options=(*options, '-OutputDirectory', str(Path(directory.name)/'revised'))
        runner=SCRIPT
        if culture:
            runner=Path(directory.name)/'culture-wrapper.ps1'
            runner.write_text("[Threading.Thread]::CurrentThread.CurrentCulture=[Globalization.CultureInfo]::GetCultureInfo('de-DE')\n& '"+str(SCRIPT)+"' @args\n")
        result = subprocess.run(['pwsh', '-NoLogo', '-NoProfile', '-NonInteractive', '-File', str(runner), '-Csv', str(trace), *scope, '-InstallRoot', r'C:\Users\agent\chk-install', *options], text=True, capture_output=True, timeout=25)
        report = (Path(directory.name)/'revised'/(trace.name+'.summary.json')) if '-BookkeepingCriterion' in options else trace.with_suffix('.csv.summary.json')
        return result, json.loads(report.read_text()) if report.exists() else None

    def test_exact_handle_tag_update_is_separate_bookkeeping(self):
        result, report = self.run_trace([
            {'Process Name': 'node.exe', 'PID': 10, 'Operation': 'RegSetInfoKey', 'Path': r'HKLM\SOFTWARE\Microsoft\Cryptography', 'Result': 'SUCCESS', 'Detail': 'KeySetInformationClass: KeySetHandleTagsInformation, Length: 0'},
            {'Process Name': 'node.exe', 'PID': 10, 'Operation': 'RegSetInfoKey', 'Path': r'HKLM', 'Result': 'SUCCESS', 'Detail': 'KeySetInformationClass: KeySetHandleTagsInformation, Length: 0'},
            {'Process Name': 'node.exe', 'PID': 10, 'Operation': 'RegSetInfoKey', 'Path': r'HKLM\SOFTWARE\Other', 'Result': 'SUCCESS', 'Detail': 'KeySetInformationClass: KeyWriteTimeInformation, Length: 8'},
        ], '-BookkeepingCriterion', 'windows-bookkeeping-v1', '-CriterionSha256', 'a' * 64)
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertEqual(report['bookkeeping_rows'], 2)
        self.assertEqual(report['outside_changes'], 1)
        self.assertEqual(report['unknown_rows'], 0)
        self.assertEqual(report['verdict'], 'FAIL')

    def test_exact_sync_and_declined_write_are_nonmutating_only_in_revised_mode(self):
        rows = [
            {'Process Name': 'pwsh.exe', 'PID': 10, 'Operation': op, 'Path': r'C:\outside.dll', 'Result': result, 'Detail': ''}
            for op, result in [('FASTIO_ACQUIRE_FOR_CC_FLUSH','SUCCESS'), ('FASTIO_RELEASE_FOR_CC_FLUSH','SUCCESS'), ('FASTIO_RELEASE_FOR_SECTION_SYNCHRONIZATION','SUCCESS'), ('FASTIO_MDL_READ_COMPLETE','SUCCESS'), ('WriteFile','FAST IO DISALLOWED'), ('FASTIO_MDL_WRITE_COMPLETE','SUCCESS'), ('WriteFile','SUCCESS')]
        ]
        result, report = self.run_trace(rows, '-BookkeepingCriterion', 'windows-bookkeeping-v1', '-CriterionSha256', 'a' * 64)
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertEqual(report['unknown_rows'], 1)
        self.assertEqual(report['outside_changes'], 1)
        self.assertEqual(report['verdict'], 'FAIL')

    def test_mapping_protection_and_scope_are_resolved_conservatively(self):
        pairs = [(r'C:\outside.dll','SyncType: SyncTypeCreateSection, PageProtection: PAGE_READONLY'), (r'C:\outside.dll','SyncType: SyncTypeOther'), (r'C:\outside.dll','SyncType: SyncTypeCreateSection, PageProtection: PAGE_READWRITE'), (r'C:\Users\agent\chk-install\cache','SyncType: SyncTypeCreateSection, PageProtection: PAGE_READWRITE')]
        rows=[{'Process Name':'pwsh.exe','PID':10,'Operation':'CreateFileMapping','Path':path,'Result':'SUCCESS','Detail':detail} for path,detail in pairs]
        result,report=self.run_trace(rows,'-BookkeepingCriterion','windows-bookkeeping-v1','-CriterionSha256','a'*64)
        self.assertEqual(result.returncode,0,result.stderr+result.stdout)
        self.assertEqual(report['unknown_rows'],1)
        self.assertEqual(report['outside_changes'],0)
        self.assertEqual(report['verdict'],'INCOMPLETE')

    def test_resolved_failure_results_do_not_hide_successful_unknown_controls(self):
        specs=[('CreateFile','NAME INVALID',''),('CreateFile','DELETE PENDING',''),('SetDispositionInformationEx','NOT EMPTY',''),('FileSystemControl','INVALID DEVICE REQUEST','Control: 0x90390 (Device:0x9 Function:228 Method: 0)'),('FileSystemControl','SUCCESS','Control: 0x90390 (Device:0x9 Function:228 Method: 0)')]
        rows=[{'Process Name':'pwsh.exe','PID':10,'Operation':op,'Path':r'C:\outside','Result':result,'Detail':detail} for op,result,detail in specs]
        result,report=self.run_trace(rows,'-BookkeepingCriterion','windows-bookkeeping-v1','-CriterionSha256','a'*64)
        self.assertEqual(result.returncode,0,result.stderr+result.stdout)
        self.assertEqual(report['unknown_rows'],1)
        self.assertEqual(report['verdict'],'INCOMPLETE')

    def test_named_pipe_communication_does_not_hide_unknown_operations(self):
        specs=[('CreatePipe',r'\Device\NamedPipe','SUCCESS',''),('SetPipeInformation',r'\Device\NamedPipe','SUCCESS',''),('WriteFile',r'\Device\NamedPipe','SUCCESS','Length: 57'),('FileSystemControl',r'\Device\NamedPipe\known','PIPE CONNECTED','Control: FSCTL_PIPE_LISTEN'),('<Unknown>',r'\Device\NamedPipe\known','SUCCESS',''),('CreatePipe',r'C:\outside','SUCCESS',''),('<Unknown>',r'\Device\NamedPipe','SUCCESS','')]
        rows=[{'Process Name':'node.exe','PID':10,'Operation':op,'Path':path,'Result':result,'Detail':detail} for op,path,result,detail in specs]
        result,report=self.run_trace(rows,'-BookkeepingCriterion','windows-bookkeeping-v1','-CriterionSha256','a'*64)
        self.assertEqual(result.returncode,0,result.stderr+result.stdout)
        # The anonymous pipe, its mode, its write, and the listen are communication; the two <Unknown>
        # rows have no raw-log proof here, and a pipe created on a file path is no pipe at all.
        self.assertEqual(report['named_pipe_rows'],4)
        self.assertEqual(report['pipe_query_rows'],0)
        self.assertEqual(report['unknown_rows'],3)
        self.assertEqual(report['outside_changes'],0)
        self.assertEqual(report['verdict'],'INCOMPLETE')

    def test_pipe_listen_is_accepted_only_for_the_exact_control_and_results(self):
        instance=r'\Device\NamedPipe\uv\0-10'
        specs=[(instance,'PIPE CONNECTED','Control: FSCTL_PIPE_LISTEN'),(instance,'SUCCESS','Control: FSCTL_PIPE_LISTEN'),(instance,'PIPE LISTENING','Control: FSCTL_PIPE_LISTEN'),(instance,'SUCCESS','Control: FSCTL_PIPE_DISCONNECT'),(instance,'SUCCESS','Control: FSCTL_PIPE_IMPERSONATE'),(r'\Device\NamedPipe','PIPE CONNECTED','Control: FSCTL_PIPE_LISTEN'),(instance,'SUCCESS','Control: FSCTL_PIPE_LISTEN_EX'),(instance,'SUCCESS','Control: 0x110008')]
        rows=[{'Process Name':'node.exe','PID':10,'Operation':'FileSystemControl','Path':path,'Result':result,'Detail':detail} for path,result,detail in specs]
        result,report=self.run_trace(rows,'-BookkeepingCriterion','windows-bookkeeping-v1','-CriterionSha256','a'*64)
        self.assertEqual(result.returncode,0,result.stderr+result.stdout)
        self.assertEqual(report['named_pipe_rows'],2)
        self.assertEqual(report['unknown_rows'],6)
        self.assertEqual(report['verdict'],'INCOMPLETE')

    def inherited_pipe_rows(self, order=('create-pipe','create-child'), creator=11, writer=12, grandchild=False):
        """inherited_pipe_rows builds the item shell 10 -> cmd 11 -> node 12 (-> node 13) tree in which cmd
        creates an anonymous pipe and a descendant sets its mode and writes to it."""
        def event(pid,operation,path,detail='',name='pwsh.exe',result='SUCCESS'):
            return {'Process Name':name,'PID':pid,'Operation':operation,'Path':path,'Result':result,'Detail':detail}
        rows=[event(10,'Process Create',r'C:\Windows\System32\cmd.exe','PID: 11, Command line: cmd /c npm.cmd'),event(11,'Process Start','','Parent PID: 10, Command line: cmd /c npm.cmd','cmd.exe')]
        if creator==13: rows.append(event(10,'Process Create',r'C:\Windows\System32\cmd.exe','PID: 13, Command line: cmd /c other'));rows.append(event(13,'Process Start','','Parent PID: 10, Command line: cmd /c other','cmd.exe'))
        steps={'create-pipe':[event(creator,'CreatePipe',r'\Device\NamedPipe','','cmd.exe')],
               'create-child':[event(11,'Process Create',r'C:\node.exe','PID: 12, Command line: node npm-prefix.js','cmd.exe'),event(12,'Process Start','','Parent PID: 11, Command line: node npm-prefix.js','node.exe')]}
        for step in order: rows+=steps[step]
        if grandchild: rows+=[event(12,'Process Create',r'C:\node.exe','PID: 14, Command line: node child.js','node.exe'),event(14,'Process Start','','Parent PID: 12, Command line: node child.js','node.exe')]
        rows+=[event(writer,'SetPipeInformation',r'\Device\NamedPipe','','node.exe'),event(writer,'WriteFile',r'\Device\NamedPipe','Length: 5','node.exe')]
        return rows

    def test_inherited_anonymous_pipe_ends_need_an_earlier_ancestor_create(self):
        result,report=self.run_trace(self.inherited_pipe_rows(),'-ShellPid','10','-BookkeepingCriterion','windows-bookkeeping-v1','-CriterionSha256','a'*64,full_export=True)
        self.assertEqual(result.returncode,0,result.stderr+result.stdout)
        self.assertEqual(report['named_pipe_rows'],3)
        self.assertEqual(report['unknown_rows'],0)
        self.assertEqual(report['verdict'],'NO_FINDINGS')
        result,report=self.run_trace(self.inherited_pipe_rows(grandchild=True,writer=14),'-ShellPid','10','-BookkeepingCriterion','windows-bookkeeping-v1','-CriterionSha256','a'*64,full_export=True)
        self.assertEqual(result.returncode,0,result.stderr+result.stdout)
        self.assertEqual(report['named_pipe_rows'],3)
        self.assertEqual(report['unknown_rows'],0)
        for name,rows in [('child created before the pipe',self.inherited_pipe_rows(order=('create-child','create-pipe'))),('no pipe created',self.inherited_pipe_rows(order=('create-child',))),('pipe created by a sibling, not an ancestor',self.inherited_pipe_rows(creator=13))]:
            with self.subTest(case=name):
                result,report=self.run_trace(rows,'-ShellPid','10','-BookkeepingCriterion','windows-bookkeeping-v1','-CriterionSha256','a'*64,full_export=True)
                self.assertEqual(result.returncode,0,result.stderr+result.stdout)
                self.assertEqual(report['unknown_rows'],2,name)
                self.assertEqual(report['verdict'],'INCOMPLETE')

    def test_bare_pipe_rows_outside_the_listed_operations_stay_unknown(self):
        rows=self.inherited_pipe_rows()+[{'Process Name':'node.exe','PID':12,'Operation':'SetEndOfFileInformationFile','Path':r'\Device\NamedPipe','Result':'SUCCESS','Detail':''},{'Process Name':'node.exe','PID':12,'Operation':'FileSystemControl','Path':r'\Device\NamedPipe','Result':'SUCCESS','Detail':'Control: FSCTL_PIPE_LISTEN'},{'Process Name':'node.exe','PID':12,'Operation':'CreateFile','Path':r'\Device\NamedPipe','Result':'SUCCESS','Detail':'Desired Access: Generic Write, Disposition: Create, OpenResult: Created'},{'Process Name':'node.exe','PID':12,'Operation':'WriteFile','Path':r'C:\outside.txt','Result':'SUCCESS','Detail':'Length: 5'}]
        result,report=self.run_trace(rows,'-ShellPid','10','-BookkeepingCriterion','windows-bookkeeping-v1','-CriterionSha256','a'*64,full_export=True)
        self.assertEqual(result.returncode,0,result.stderr+result.stdout)
        self.assertEqual(report['named_pipe_rows'],3)
        self.assertEqual(report['unknown_rows'],3)
        self.assertEqual(report['outside_changes'],1)
        self.assertEqual(report['verdict'],'FAIL')

    def test_kernel_cache_requires_matching_stacks_and_exact_ea_query(self):
        target=r'C:\Program Files\PowerShell\7\Microsoft.CodeAnalysis.dll'
        rows=[{'Process Name':'pwsh.exe','PID':10,'Operation':op,'Path':target,'Result':'SUCCESS','Detail':detail} for op,detail in [('SetEAFile',''),('FileSystemControl','Control: FSCTL_WRITE_USN_CLOSE_RECORD')]]
        metadata={'events':[{'pid':10,'operation':row['Operation'],'path':target,'result':'SUCCESS','detail':row['Detail'],'frames':[{'path':r'C:\Windows\System32\CI.dll'}]} for row in rows],'ea':[{'file':'Microsoft.CodeAnalysis.dll','queryExit':0,'eaNames':['$KERNEL.PURGE.ESBCACHE'],'capturedUtc':'2026-10-22T05:22:05Z'}]}
        result,report=self.run_trace(rows,'-BookkeepingCriterion','windows-bookkeeping-v1','-CriterionSha256','a'*64,full_export=True,metadata=metadata)
        self.assertEqual(result.returncode,0,result.stderr+result.stdout)
        self.assertEqual(report['bookkeeping_rows'],2)
        self.assertEqual(report['unknown_rows'],0)
        self.assertEqual(report['outside_changes'],0)
        self.assertEqual(report['verdict'],'NO_FINDINGS')
        metadata['events'][0]['frames']=[{'path':r'C:\attacker\CI.dll'}]
        result,report=self.run_trace(rows,'-BookkeepingCriterion','windows-bookkeeping-v1','-CriterionSha256','a'*64,full_export=True,metadata=metadata)
        self.assertEqual(result.returncode,0,result.stderr+result.stdout)
        self.assertEqual(report['bookkeeping_rows'],0)
        self.assertEqual(report['verdict'],'INCOMPLETE')

    def test_cache_utc_query_survives_non_us_culture(self):
        target=r'C:\Program Files\PowerShell\7\Microsoft.CodeAnalysis.dll'
        row={'Process Name':'pwsh.exe','PID':10,'Operation':'SetEAFile','Path':target,'Result':'SUCCESS','Detail':''}
        metadata={'events':[{'pid':10,'operation':'SetEAFile','path':target,'result':'SUCCESS','detail':'','frames':[{'path':r'C:\Windows\System32\CI.dll'}]}],'ea':[{'file':'Microsoft.CodeAnalysis.dll','queryExit':0,'eaNames':['$KERNEL.PURGE.ESBCACHE'],'capturedUtc':'2026-10-22T05:22:05.1360116Z'}]}
        result,report=self.run_trace([row],'-BookkeepingCriterion','windows-bookkeeping-v1','-CriterionSha256','a'*64,full_export=True,metadata=metadata,culture='de-DE')
        self.assertEqual(result.returncode,0,result.stderr+result.stdout)
        self.assertEqual(report['bookkeeping_rows'],1)
        self.assertEqual(report['verdict'],'NO_FINDINGS')

    def test_cache_query_rejects_other_names_failed_query_and_missing_time(self):
        target=r'C:\Program Files\PowerShell\7\Microsoft.CodeAnalysis.dll'
        row={'Process Name':'pwsh.exe','PID':10,'Operation':'SetEAFile','Path':target,'Result':'SUCCESS','Detail':''}
        for names,query_exit,time in [(['$KERNEL.PURGE.OTHER'],0,'2026-10-22T05:22:05Z'), (['$KERNEL.PURGE.ESBCACHE','other'],0,'2026-10-22T05:22:05Z'), (['$KERNEL.PURGE.ESBCACHE'],1,'2026-10-22T05:22:05Z'), (['$KERNEL.PURGE.ESBCACHE'],0,'')]:
            with self.subTest(names=names,query_exit=query_exit,time=time):
                metadata={'events':[{'pid':10,'operation':'SetEAFile','path':target,'result':'SUCCESS','detail':'','frames':[{'path':r'C:\Windows\System32\CI.dll'}]}],'ea':[{'file':'Microsoft.CodeAnalysis.dll','queryExit':query_exit,'eaNames':names,'capturedUtc':time}]}
                result,report=self.run_trace([row],'-BookkeepingCriterion','windows-bookkeeping-v1','-CriterionSha256','a'*64,full_export=True,metadata=metadata)
                self.assertEqual(result.returncode,0,result.stderr+result.stdout)
                self.assertEqual(report['bookkeeping_rows'],0)
                self.assertEqual(report['verdict'],'INCOMPLETE')

    def test_journal_path_and_flags_without_stack_do_not_qualify(self):
        rows=[{'Process Name':'pwsh.exe','PID':10,'Operation':'WriteFile','Path':r'C:\$LogFile','Result':'SUCCESS','Detail':'Non-cached, Paging I/O'}, {'Process Name':'pwsh.exe','PID':10,'Operation':'SetEAFile','Path':r'C:\arbitrary.dll','Result':'SUCCESS','Detail':''}, {'Process Name':'pwsh.exe','PID':10,'Operation':'WriteFile','Path':r'C:\user-data.txt','Result':'SUCCESS','Detail':'Length: 5'}]
        result,report=self.run_trace(rows,'-BookkeepingCriterion','windows-bookkeeping-v1','-CriterionSha256','a'*64)
        self.assertEqual(result.returncode,0,result.stderr+result.stdout)
        self.assertEqual(report['bookkeeping_rows'],0)
        self.assertEqual(report['unknown_rows'],2)
        self.assertEqual(report['outside_changes'],1)
        self.assertEqual(report['verdict'],'FAIL')

    def test_other_file_write_disqualifies_kernel_metadata_family(self):
        target=r'C:\Program Files\PowerShell\7\Microsoft.CodeAnalysis.dll'
        rows=[{'Process Name':'pwsh.exe','PID':10,'Operation':'SetEAFile','Path':target,'Result':'SUCCESS','Detail':''}, {'Process Name':'pwsh.exe','PID':10,'Operation':'WriteFile','Path':target,'Result':'SUCCESS','Detail':'Length: 5'}]
        metadata={'events':[{'pid':10,'operation':'SetEAFile','path':target,'result':'SUCCESS','detail':'','frames':[{'path':r'C:\Windows\System32\CI.dll'}]}],'ea':[{'file':'Microsoft.CodeAnalysis.dll','queryExit':0,'eaNames':['$KERNEL.PURGE.ESBCACHE'],'capturedUtc':'2026-10-22T05:22:05Z'}]}
        result,report=self.run_trace(rows,'-BookkeepingCriterion','windows-bookkeeping-v1','-CriterionSha256','a'*64,full_export=True,metadata=metadata)
        self.assertEqual(result.returncode,0,result.stderr+result.stdout)
        self.assertEqual(report['bookkeeping_rows'],0)
        self.assertEqual(report['outside_changes'],1)
        self.assertEqual(report['verdict'],'FAIL')

    def test_revised_policy_requires_separate_output_and_a_criterion_hash(self):
        row={'Process Name':'pwsh.exe','PID':10,'Operation':'ReadFile','Path':r'C:\readme','Result':'SUCCESS','Detail':''}
        result,report=self.run_trace([row],'-BookkeepingCriterion','windows-bookkeeping-v1')
        self.assertNotEqual(result.returncode,0)
        self.assertIsNone(report)

    def test_journal_needs_system_ntfs_frames_and_no_direct_open(self):
        write={'Process Name':'pwsh.exe','PID':10,'Operation':'WriteFile','Path':r'C:\$LogFile','Result':'SUCCESS','Detail':'Non-cached, Paging I/O'}
        metadata={'events':[{'pid':10,'operation':'WriteFile','path':r'C:\$LogFile','result':'SUCCESS','detail':write['Detail'],'frames':[{'path':r'C:\Windows\System32\drivers\Ntfs.sys'}]}],'ea':[],'ntfs_volume_roots':['C:\\']}
        result,report=self.run_trace([write],'-BookkeepingCriterion','windows-bookkeeping-v1','-CriterionSha256','a'*64,full_export=True,metadata=metadata)
        self.assertEqual(result.returncode,0,result.stderr+result.stdout)
        self.assertEqual(report['bookkeeping_rows'],1)
        self.assertEqual(report['verdict'],'NO_FINDINGS')
        opened={'Process Name':'pwsh.exe','PID':10,'Operation':'CreateFile','Path':r'C:\$LogFile','Result':'SUCCESS','Detail':'OpenResult: Opened'}
        result,report=self.run_trace([opened,write],'-BookkeepingCriterion','windows-bookkeeping-v1','-CriterionSha256','a'*64,full_export=True,metadata=metadata)
        self.assertEqual(result.returncode,0,result.stderr+result.stdout)
        self.assertEqual(report['bookkeeping_rows'],0)
        self.assertEqual(report['verdict'],'FAIL')

    def test_counts_exported_rows_without_expected_rows(self):
        result, report = self.run_trace([dict(zip(['Process Name', 'PID', 'Operation', 'Path', 'Result', 'Detail'], ['pwsh.exe', 10, 'ReadFile', r'C:\outside.txt', 'SUCCESS', 'Length: 1']))])
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertEqual(report['exported_rows'], 1)
        self.assertEqual(report['operations'], [{'operation': 'ReadFile', 'rows': 1}])

    def test_derives_children_and_excludes_unrelated_writes(self):
        def event(pid, operation, path, detail='', name='pwsh.exe'):
            return {'Process Name': name, 'PID': pid, 'Operation': operation, 'Path': path, 'Result': 'SUCCESS', 'Detail': detail}
        result, report = self.run_trace([
            event(10, 'Process Create', r'C:\node.exe', 'PID: 11, Command line: node install.js'),
            event(11, 'Process Start', '', 'Parent PID: 10, Command line: node install.js', 'node.exe'),
            event(11, 'WriteFile', r'C:\Users\agent\chk-install\prefix\file', 'Length: 3', 'node.exe'),
            event(99, 'WriteFile', r'C:\outside.txt', 'Length: 2', 'unrelated.exe'),
        ], '-ShellPid', '10')
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertEqual(report['process_ids'], [10, 11])
        self.assertEqual(report['exported_rows'], 4)
        self.assertEqual(report['filesystem_registry_rows'], 1)
        self.assertEqual(report['outside_changes'], 0)

    def test_nuget_default_allows_only_default_directory_and_path_value(self):
        rows = [
            {'Process Name': 'pwsh.exe', 'PID': 10, 'Operation': 'RegSetValue', 'Path': r'HKCU\Environment\Path', 'Result': 'SUCCESS', 'Detail': 'Type: REG_SZ'},
            {'Process Name': 'pwsh.exe', 'PID': 10, 'Operation': 'WriteFile', 'Path': r'C:\Users\agent\AppData\Local\Programs\sandboxed-agents\sandboxed-agents.exe', 'Result': 'SUCCESS', 'Detail': 'Length: 3'},
            {'Process Name': 'pwsh.exe', 'PID': 10, 'Operation': 'RegSetValue', 'Path': r'HKCU\Environment\OTHER', 'Result': 'SUCCESS', 'Detail': 'Type: REG_SZ'},
        ]
        result, report = self.run_trace(rows, '-Method', 'nuget-default', '-DefaultInstallRoot', r'C:\Users\agent\AppData\Local\Programs\sandboxed-agents')
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertEqual(report['outside_changes'], 1)
        self.assertEqual(report['allowed_changes'], 2)

    def test_documented_nuget_default_programs_parent_is_allowed(self):
        result, report = self.run_trace([
            {'Process Name': 'pwsh.exe', 'PID': 10, 'Operation': 'CreateFile', 'Path': r'C:\Users\agent\AppData\Local\Programs', 'Result': 'SUCCESS', 'Detail': 'Options: Directory, OpenResult: Created'},
            {'Process Name': 'pwsh.exe', 'PID': 10, 'Operation': 'WriteFile', 'Path': r'C:\Users\agent\AppData\Local\Programs\unrelated\file', 'Result': 'SUCCESS', 'Detail': 'Length: 1'},
            {'Process Name': 'pwsh.exe', 'PID': 10, 'Operation': 'SetEAFile', 'Path': r'C:\Program Files\PowerShell\7\Microsoft.CodeAnalysis.dll', 'Result': 'SUCCESS', 'Detail': ''},
        ], '-Method', 'nuget-default', '-DefaultInstallRoot', r'C:\Users\agent\AppData\Local\Programs\sandboxed-agents')
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertEqual(report['allowed_changes'], 1)
        self.assertEqual(report['outside_changes'], 2)
        self.assertEqual(report['verdict'], 'FAIL')

    def test_programs_parent_is_not_allowed_for_other_methods(self):
        result, report = self.run_trace([
            {'Process Name': 'pwsh.exe', 'PID': 10, 'Operation': 'CreateFile', 'Path': r'C:\Users\agent\AppData\Local\Programs', 'Result': 'SUCCESS', 'Detail': 'Options: Directory, OpenResult: Created'},
        ], '-Method', 'nuget-root')
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertEqual(report['outside_changes'], 1)

    def test_file_close_and_documented_query_fsctls_are_resolved(self):
        rows = [
            {'Process Name': 'pwsh.exe', 'PID': 10, 'Operation': 'IRP_MJ_CLOSE', 'Path': r'C:\Windows\System32\AppLocker', 'Result': 'SUCCESS', 'Detail': ''},
            {'Process Name': 'pwsh.exe', 'PID': 10, 'Operation': 'FileSystemControl', 'Path': r'C:\Users\agent\chk-install\package\tools\binary.exe', 'Result': 'NOT SUPPORTED', 'Detail': 'Control: FSCTL_OFFLOAD_READ, Offset: 0, Length: 9437184'},
            {'Process Name': 'pwsh.exe', 'PID': 10, 'Operation': 'FileSystemControl', 'Path': r'C:\outside.dll', 'Result': 'SUCCESS', 'Detail': 'Control: FSCTL_QUERY_USN_JOURNAL'},
            {'Process Name': 'pwsh.exe', 'PID': 10, 'Operation': 'FileSystemControl', 'Path': r'C:\outside.dll', 'Result': 'SUCCESS', 'Detail': 'Control: FSCTL_READ_FILE_USN_DATA'},
        ]
        result, report = self.run_trace(rows, '-ShellPid', '10', '-Method', 'nuget-root', full_export=True)
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertEqual(report['unknown_rows'], 0)
        self.assertEqual(report['verdict'], 'NO_FINDINGS')

    def test_read_fsctl_allowlist_does_not_hide_write_or_unknown_controls(self):
        rows = [
            {'Process Name': 'pwsh.exe', 'PID': 10, 'Operation': 'FileSystemControl', 'Path': r'C:\outside.dll', 'Result': 'SUCCESS', 'Detail': 'Control: FSCTL_WRITE_USN_CLOSE_RECORD'},
            {'Process Name': 'pwsh.exe', 'PID': 10, 'Operation': 'FileSystemControl', 'Path': r'C:\outside.dll', 'Result': 'SUCCESS', 'Detail': 'Control: FSCTL_QUERY_USN_JOURNAL_UNKNOWN'},
        ]
        result, report = self.run_trace(rows)
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertEqual(report['unknown_rows'], 2)
        self.assertEqual(report['verdict'], 'INCOMPLETE')

    def test_absolute_podman_child_fails_even_when_no_fake_call_file_exists(self):
        result, report = self.run_trace([
            {'Process Name': 'pwsh.exe', 'PID': 10, 'Operation': 'Process Create', 'Path': r'C:\Tools\podman.exe', 'Result': 'SUCCESS', 'Detail': 'PID: 11, Command line: C:\\Tools\\podman.exe info'},
            {'Process Name': 'renamed.exe', 'PID': 11, 'Operation': 'Process Start', 'Path': '', 'Result': 'SUCCESS', 'Detail': 'Parent PID: 10, Command line: C:\\Tools\\podman.exe info'},
            {'Process Name': 'pwsh.exe', 'PID': 10, 'Operation': 'ReadFile', 'Path': r'C:\read.txt', 'Result': 'SUCCESS', 'Detail': ''},
        ], '-ShellPid', '10')
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertEqual(report['podman_processes'], 1)
        self.assertEqual(report['verdict'], 'FAIL')

    def test_fake_podman_environment_is_not_an_executed_podman(self):
        result, report = self.run_trace([
            {'Process Name': 'pwsh.exe', 'PID': 10, 'Operation': 'Process Create', 'Path': r'C:\Users\agent\chk-install\bin\sandboxed-agents.exe', 'Result': 'SUCCESS', 'Detail': r'PID: 11, Command line: C:\Users\agent\chk-install\bin\sandboxed-agents.exe version'},
            {'Process Name': 'sandboxed-agents.exe', 'PID': 11, 'Operation': 'Process Start', 'Path': '', 'Result': 'SUCCESS', 'Detail': r'Parent PID: 10, Command line: C:\Users\agent\chk-install\bin\sandboxed-agents.exe version, Current directory: C:\traces, Environment: PATH=C:\traces\chk-fake-podman-1948;C:\Tools'},
            {'Process Name': 'pwsh.exe', 'PID': 10, 'Operation': 'ReadFile', 'Path': r'C:\read.txt', 'Result': 'SUCCESS', 'Detail': ''},
        ], '-ShellPid', '10')
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertEqual(report['podman_processes'], 0)
        self.assertEqual(report['verdict'], 'NO_FINDINGS')

    def test_podman_named_directory_is_not_a_podman_executable(self):
        result, report = self.run_trace([
            {'Process Name': 'pwsh.exe', 'PID': 10, 'Operation': 'Process Create', 'Path': r'C:\podman\node.exe', 'Result': 'SUCCESS', 'Detail': r'PID: 11, Command line: C:\podman\node.exe --version'},
            {'Process Name': 'node.exe', 'PID': 11, 'Operation': 'Process Start', 'Path': '', 'Result': 'SUCCESS', 'Detail': r'Parent PID: 10, Command line: C:\podman\node.exe --version'},
            {'Process Name': 'pwsh.exe', 'PID': 10, 'Operation': 'ReadFile', 'Path': r'C:\read.txt', 'Result': 'SUCCESS', 'Detail': ''},
        ], '-ShellPid', '10')
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertEqual(report['podman_processes'], 0)

    def test_shell_absolute_podman_command_is_still_detected(self):
        result, report = self.run_trace([
            {'Process Name': 'pwsh.exe', 'PID': 10, 'Operation': 'Process Create', 'Path': r'C:\Windows\System32\cmd.exe', 'Result': 'SUCCESS', 'Detail': r'PID: 11, Command line: cmd.exe /c C:\Tools\podman.cmd info'},
            {'Process Name': 'cmd.exe', 'PID': 11, 'Operation': 'Process Start', 'Path': '', 'Result': 'SUCCESS', 'Detail': r'Parent PID: 10, Command line: cmd.exe /c C:\Tools\podman.cmd info, Environment: PATH=C:\Tools'},
            {'Process Name': 'pwsh.exe', 'PID': 10, 'Operation': 'ReadFile', 'Path': r'C:\read.txt', 'Result': 'SUCCESS', 'Detail': ''},
        ], '-ShellPid', '10')
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertEqual(report['podman_processes'], 1)
        self.assertEqual(report['verdict'], 'FAIL')

    def test_ntfs_journal_write_remains_an_outside_failure(self):
        result, report = self.run_trace([
            {'Process Name': 'pwsh.exe', 'PID': 10, 'Operation': 'WriteFile', 'Path': r'C:\$LogFile', 'Result': 'SUCCESS', 'Detail': 'Length: 110592, I/O Flags: Paging I/O'},
        ])
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertEqual(report['outside_changes'], 1)
        self.assertEqual(report['verdict'], 'FAIL')

    def test_rename_checks_both_paths_without_fixed_expected_count(self):
        result, report = self.run_trace([
            {'Process Name': 'pwsh.exe', 'PID': 10, 'Operation': 'SetRenameInformationFile', 'Path': r'C:\Users\agent\chk-install\old', 'Result': 'SUCCESS', 'Detail': r'ReplaceIfExists: True, FileName: C:\Users\agent\chk-install\new'},
            {'Process Name': 'pwsh.exe', 'PID': 10, 'Operation': 'SetRenameInformationFile', 'Path': r'C:\Users\agent\chk-install\old2', 'Result': 'SUCCESS', 'Detail': r'ReplaceIfExists: True, FileName: C:\outside\new'},
        ])
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertEqual(report['allowed_changes'], 1)
        self.assertEqual(report['outside_changes'], 1)

    def test_unknown_operation_never_becomes_a_pass(self):
        result, report = self.run_trace([
            {'Process Name': 'pwsh.exe', 'PID': 10, 'Operation': 'NewFilesystemOperation', 'Path': r'C:\Users\agent\chk-install\file', 'Result': 'SUCCESS', 'Detail': 'Unknown: 1'},
        ])
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertEqual(report['unknown_rows'], 1)
        self.assertEqual(report['verdict'], 'INCOMPLETE')

    def test_create_and_plain_failure_and_sibling_boundary(self):
        result, report = self.run_trace([
            {'Process Name': 'pwsh.exe', 'PID': 10, 'Operation': 'CreateFile', 'Path': r'C:\Users\agent\chk-install\new', 'Result': 'SUCCESS', 'Detail': 'OpenResult: Created'},
            {'Process Name': 'pwsh.exe', 'PID': 10, 'Operation': 'WriteFile', 'Path': r'C:\Users\agent\chk-install-escape\file', 'Result': 'SUCCESS', 'Detail': 'Length: 1'},
            {'Process Name': 'pwsh.exe', 'PID': 10, 'Operation': 'WriteFile', 'Path': r'C:\outside.txt', 'Result': 'ACCESS DENIED', 'Detail': 'Length: 1'},
        ])
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertEqual(report['allowed_changes'], 1)
        self.assertEqual(report['outside_changes'], 1)

    def test_reused_child_pid_stops_attribution(self):
        rows = [{'Process Name': 'pwsh.exe', 'PID': 10, 'Operation': 'ReadFile', 'Path': r'C:\read', 'Result': 'SUCCESS', 'Detail': ''}]
        rows += [{'Process Name': 'node.exe', 'PID': 11, 'Operation': 'Process Start', 'Path': '', 'Result': 'SUCCESS', 'Detail': 'Parent PID: 10'}] * 2
        result, report = self.run_trace(rows, '-ShellPid', '10')
        self.assertNotEqual(result.returncode, 0)
        self.assertIsNone(report)
        self.assertIn('reused', result.stderr)

    def test_process_filtered_csv_cannot_establish_complete_scope(self):
        result, report = self.run_trace([
            {'Process Name': 'pwsh.exe', 'PID': 10, 'Operation': 'ReadFile', 'Path': r'C:\read.txt', 'Result': 'SUCCESS', 'Detail': ''},
        ], '-ShellPid', '10')
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertEqual(report['verdict'], 'INCOMPLETE')

    def test_full_export_allows_nuget_method_without_a_child_process(self):
        result, report = self.run_trace([
            {'Process Name': 'pwsh.exe', 'PID': 10, 'Operation': 'WriteFile', 'Path': r'C:\Users\agent\chk-install\nuget\sandboxed-agents.exe', 'Result': 'SUCCESS', 'Detail': 'Length: 1'},
        ], '-ShellPid', '10', '-Method', 'nuget-root', full_export=True)
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertEqual(report['verdict'], 'NO_FINDINGS')

    def test_registry_key_named_path_is_not_the_allowed_path_value(self):
        result, report = self.run_trace([
            {'Process Name': 'pwsh.exe', 'PID': 10, 'Operation': 'RegDeleteKey', 'Path': r'HKCU\Environment\Path', 'Result': 'SUCCESS', 'Detail': ''},
        ], '-Method', 'nuget-default', '-DefaultInstallRoot', r'C:\Users\agent\AppData\Local\Programs\sandboxed-agents')
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertEqual(report['outside_changes'], 1)


if __name__ == '__main__':
    unittest.main()
