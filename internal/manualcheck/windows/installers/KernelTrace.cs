using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Linq;
using System.Text;
using System.Text.RegularExpressions;
using System.Xml;
using System.Xml.Linq;

// Saved-export reader. Every acceptance fact is derived from CSV/XML, never a supplied valid flag.
public static class InstallerKernelTrace
{
    const string Header="68fdd900-4a3e-11d1-84f4-0000f80464e3", FileIo="90cbdc39-4a3e-11d1-84f4-0000f80464e3", StackWalk="def2fe46-7bd6-4b80-bd94-f57fe20d0ce3", Image="2cb15d1d-5fc1-11d2-abe1-00a0c911f518", Process="3d6fa8d0-fe05-11d0-9dda-00c04fd7ba7c", Thread="3d6fa8d1-fe05-11d0-9dda-00c04fd7ba7c";
    const string Ntfs=@"\SystemRoot\System32\Drivers\Ntfs.sys";
    const uint Unset=uint.MaxValue;
    public class Pm { public long Ordinal; public uint Pid,Tid; public string Op,Path,Result,Detail,Name; public string[] Frames; }
    class Event { public long Index; public ulong Time; public uint Pid,Tid; public int Op,Version; public string Guid; public Dictionary<string,string> Fields; public byte[] Raw; }
    class Write { public Event E; public uint Tid,Size,Flags; public ulong Key,Offset; }
    class Stack { public Event E; public ulong Time; public uint Pid,Tid; public ulong[] Frames; }
    class Life { public Event E; public uint Pid,Tid; public string Key; }
    class Module { public Event E; public ulong Base,Size; public string Path; public uint Pid; }
    static XElement Child(XElement x,string name) { return x?.Elements().SingleOrDefault(e=>e.Name.LocalName==name); }
    static string Val(XElement x,string name) { return Child(x,name)?.Value; }
    static void Need(bool ok,string message) { if(!ok) throw new FormatException(message); }
    static ulong Num(string s) { Need(s!=null,"Required numeric field absent");s=s.Trim();return s.StartsWith("0x",StringComparison.OrdinalIgnoreCase)?ulong.Parse(s.Substring(2),NumberStyles.HexNumber,CultureInfo.InvariantCulture):ulong.Parse(s,CultureInfo.InvariantCulture); }
    static uint U32(string s) { return checked((uint)Num(s)); }
    static string Field(Event e,string name) { Need(e.Fields.ContainsKey(name),"Required "+name+" field absent");return e.Fields[name]; }
    static string Id(ulong time,uint tid) { return time.ToString(CultureInfo.InvariantCulture)+":"+tid; }
    static bool Equal(string a,string b) { return string.Equals(a,b,StringComparison.OrdinalIgnoreCase); }
    static bool Range(ulong address,ulong start,ulong size) { return size>0&&address>=start&&address-start<size; }
    static IEnumerable<XElement> Elements(string file,string name)
    {
        using(var r=XmlReader.Create(file,new XmlReaderSettings{DtdProcessing=DtdProcessing.Prohibit,XmlResolver=null}))
            while(r.Read()) if(r.NodeType==XmlNodeType.Element&&r.LocalName==name) {
                XElement e; using(var sub=r.ReadSubtree()) e=XElement.Load(sub); yield return e;
            }
    }
    // Stream RFC4180 records, including embedded newlines and escaped quotes, without culture conversion.
    static IEnumerable<string[]> Csv(string file)
    {
        using(var r=new StreamReader(file)) {
            var fields=new List<string>();var b=new StringBuilder();bool quoted=false,closed=false,begin=true;int c;
            while((c=r.Read())!=-1) {
                char ch=(char)c;
                if(quoted) { if(ch=='"') { if(r.Peek()=='"') { r.Read();b.Append('"'); } else { quoted=false;closed=true; } } else b.Append(ch);continue; }
                if(ch=='"') { Need(begin&&!closed,"Malformed CSV quote");quoted=true;begin=false;continue; }
                if(ch==','||ch=='\r'||ch=='\n') {
                    fields.Add(b.ToString());b.Clear();begin=true;closed=false;
                    if(ch!=',') { if(ch=='\r'&&r.Peek()=='\n')r.Read();yield return fields.ToArray();fields.Clear(); }
                    continue;
                }
                Need(!closed,"Characters after CSV closing quote");b.Append(ch);begin=false;
            }
            Need(!quoted,"Unclosed CSV quote");if(fields.Count>0||b.Length>0||closed) { fields.Add(b.ToString());yield return fields.ToArray(); }
        }
    }
    // XML1.0 normalizes literal CRLF and CR to LF. Apply that same encoding rule to the CSV field.
    static string XmlNewlines(string text) { return text.Replace("\r\n","\n").Replace("\r","\n"); }
    // Procmon CSV uses semicolons where XML retains string-data/entry line feeds.
    // Preserve every other character, matching literal newlines, and all candidate I/O details.
    static bool SameCell(string csv,string xml,string column,string operation)
    {
        if(xml==null)return false;csv=XmlNewlines(csv);xml=XmlNewlines(xml);if(csv==xml)return true;
        // XML export replaces surrogate units in registry names with underscores.
        // Only dismissed read-only registry rows can use that display form, never changing/stack evidence.
        string[] registryReads={"RegOpenKey","RegQueryKey","RegQueryValue","RegEnumKey","RegEnumValue","RegCloseKey","RegQueryKeySecurity","RegFlushKey"};
        if(column=="Path"&&registryReads.Contains(operation)&&csv.Length==xml.Length&&Regex.IsMatch(csv,@"^(?:HKLM|HKCU|HKU|HKCR|HKCC)\\",RegexOptions.IgnoreCase)) {
            for(int i=0;i<csv.Length;i++)if(csv[i]!=xml[i]&&!(char.IsSurrogate(csv[i])&&xml[i]=='_'))return false;
            return true;
        }
        if(column!="Detail"||csv.Length!=xml.Length)return false;
        int delimiterStart=-1;
        if(operation=="Process Start") {
            const string marker="Environment:";int a=csv.LastIndexOf(marker,StringComparison.Ordinal),b=xml.LastIndexOf(marker,StringComparison.Ordinal);
            if(a<0||a!=b)return false;delimiterStart=a+marker.Length;
        } else if(operation=="RegSetValue"||operation=="RegQueryValue") {
            var prefix=Regex.Match(csv,@"^Type: (?:REG_SZ|REG_EXPAND_SZ|REG_MULTI_SZ), Length: [0-9.,]+, Data: ");
            if(!prefix.Success)return false;delimiterStart=prefix.Length;
        } else return false;
        for(int i=0;i<csv.Length;i++)if(csv[i]!=xml[i]&&!(i>=delimiterStart&&csv[i]==';'&&xml[i]=='\n'))return false;
        return true;
    }
    public static List<Pm> ReadProcmon(string csv,string xml)
    {
        var result=new List<Pm>();using(var records=Csv(csv).GetEnumerator()) {
            Need(records.MoveNext(),"CSV empty");var columns=records.Current;
            string[] required={"Process Name","PID","TID","Operation","Path","Result","Detail"};
            Need(columns.Distinct().Count()==columns.Length&&required.All(columns.Contains),"CSV requires unique identity and TID columns");
            long ordinal=0;
            foreach(var e in Elements(xml,"event")) {
                ordinal++;Need(records.MoveNext(),"Procmon CSV/XML event count mismatch");var values=records.Current;Need(values.Length==columns.Length,"CSV column count mismatch");
                var d=columns.Select((k,i)=>new{k,v=values[i]}).ToDictionary(x=>x.k,x=>x.v);
                foreach(string k in columns) {
                    string a=d[k],b=Val(e,Regex.Replace(k,@"[^A-Za-z0-9_]","_"));
                    Need(SameCell(a,b,k,d["Operation"]),"Procmon CSV/XML ordinal identity mismatch at "+ordinal+" field "+k+" operation "+d["Operation"]+" lengths "+a.Length+"/"+(b==null?-1:b.Length));
                }
                result.Add(new Pm{Ordinal=ordinal,Pid=U32(d["PID"]),Tid=U32(d["TID"]),Op=d["Operation"],Path=d["Path"],Result=d["Result"],Detail=d["Detail"],Name=d["Process Name"],Frames=(d["Operation"]=="SetEAFile"||d["Operation"]=="FileSystemControl"||(d["Operation"]=="WriteFile"&&Regex.IsMatch(d["Path"],@"^[A-Za-z]:\\\$LogFile$")))?e.Descendants().Where(f=>f.Name.LocalName=="frame").Select(f=>Val(f,"path")??"").ToArray():Array.Empty<string>()});
            }
            Need(!records.MoveNext()&&ordinal>0,"Procmon CSV/XML event count mismatch");
        }return result;
    }
    static IEnumerable<Event> ReadEvents(string file)
    {
        long i=0;foreach(var x in Elements(file,"Event")) {
            var s=Child(x,"System");Need(s!=null,"ETW System absent");
            string guid=Val(Child(x,"ExtendedTracingInfo"),"EventGuid");Need(guid!=null,"ETW task EventGuid absent");guid=guid.Trim('{','}').ToLowerInvariant();
            int op=int.Parse(Val(s,"Opcode"),CultureInfo.InvariantCulture),version=int.Parse(Val(s,"Version"),CultureInfo.InvariantCulture);
            bool proofPayload=(guid==Header&&op==0)||(guid==FileIo&&(op==0||op==32||op==35||op==36||op==68))||guid==StackWalk||guid==Image||((guid==Process||guid==Thread)&&op>=1&&op<=4);
            var f=new Dictionary<string,string>();var data=Child(x,"EventData");
            // Other records may contain legitimate arrays. Their headers still participate in every collision/count check.
            if(proofPayload&&data!=null)foreach(var d in data.Elements()) { string name=(string)d.Attribute("Name");Need(name!=null&&!f.ContainsKey(name),"Duplicate/unnamed ETW proof field at event "+(i+1)+" task "+guid+" opcode "+op);f.Add(name,d.Value); }
            var exec=Child(s,"Execution");string time=(string)Child(s,"TimeCreated")?.Attribute("RawTime");Need(time!=null,"ETW RawTime absent; no clock conversion permitted");
            string hex=Val(Child(x,"ProcessingErrorData"),"EventPayload");
            yield return new Event{Index=++i,Time=Num(time),Pid=U32((string)exec?.Attribute("ProcessID")??"4294967295"),Tid=U32((string)exec?.Attribute("ThreadID")??"4294967295"),Version=version,Op=op,Guid=guid,Fields=f,Raw=hex==null?null:Convert.FromHexString(hex)};
        }
    }
    static Write DecodeWrite(Event e)
    {
        Need(BitConverter.IsLittleEndian&&e.Version==3&&e.Raw!=null&&(e.Raw.Length==44||e.Raw.Length==48),"Unsupported FileIo write layout");
        return new Write{E=e,Offset=BitConverter.ToUInt64(e.Raw,0),Key=BitConverter.ToUInt64(e.Raw,24),Tid=BitConverter.ToUInt32(e.Raw,32),Size=BitConverter.ToUInt32(e.Raw,36),Flags=BitConverter.ToUInt32(e.Raw,40)};
    }
    // MaxFrames is the deepest stack walk the kernel event carries on the captured hosts: 256 frames,
    // observed in the saved traces; a longer payload is not a stack walk this reader understands.
    const int MaxFrames=256;
    static Stack DecodeStack(Event e)
    {
        Need(e.Op==32&&e.Version==2,"Unsupported StackWalk opcode/version");var s=new Stack{E=e};
        if(e.Raw!=null) {
            // 16 header bytes (timestamp, process, thread) then 8-byte frames. The kernel delivers up to
            // MaxFrames frames; the npm capture of 2026-10-22 held raw stacks of 193 to 256 frames.
            Need(e.Fields.Count==0&&e.Raw.Length>=24&&(e.Raw.Length-16)%8==0&&(e.Raw.Length-16)/8<=MaxFrames,"Unsupported raw StackWalk layout");
            s.Time=BitConverter.ToUInt64(e.Raw,0);s.Pid=BitConverter.ToUInt32(e.Raw,8);s.Tid=BitConverter.ToUInt32(e.Raw,12);
            s.Frames=Enumerable.Range(0,(e.Raw.Length-16)/8).Select(i=>BitConverter.ToUInt64(e.Raw,16+i*8)).ToArray();
        } else {
            int count=e.Fields.Count-3;Need(count>=1&&count<=MaxFrames,"Empty/unknown named StackWalk fields");
            s.Time=Num(Field(e,"EventTimeStamp"));s.Pid=U32(Field(e,"StackProcess"));s.Tid=U32(Field(e,"StackThread"));
            s.Frames=Enumerable.Range(1,count).Select(i=>Num(Field(e,"Stack"+i))).ToArray();
        }
        Need((e.Pid==Unset||e.Pid==s.Pid)&&(e.Tid==Unset||e.Tid==s.Tid),"StackWalk header/payload identity disagreement");return s;
    }
    static Life DecodeLife(Event e)
    {
        uint pid,tid=0;string key="";
        if(e.Guid==Process) { Need(e.Version==4&&e.Raw==null,"Unsupported process lifetime layout");pid=U32(Field(e,"ProcessId"));key=Field(e,"UniqueProcessKey");Num(key); }
        else if(e.Raw!=null) { Need(e.Version==3&&e.Raw.Length>=68,"Unsupported raw thread lifetime layout");pid=BitConverter.ToUInt32(e.Raw,0);tid=BitConverter.ToUInt32(e.Raw,4); }
        else { Need(e.Version==3,"Unsupported named thread lifetime layout");pid=U32(Field(e,"ProcessId"));tid=U32(Field(e,"TThreadId")); }
        return new Life{E=e,Pid=pid,Tid=tid,Key=key};
    }
    static void SingleLife(List<Life> rows,string label,bool full=false)
    {
        Need(rows.Count>0,label+" lifetime missing");
        Need(rows.Select(x=>x.Pid).Distinct().Count()==1&&rows.Select(x=>x.Key).Distinct().Count()==1,label+" reused or association changed");
        var start=rows.Where(x=>x.E.Op==1).ToList();var end=rows.Where(x=>x.E.Op==2).ToList();var rundown=rows.Where(x=>x.E.Op==3).ToList();var stopRundown=rows.Where(x=>x.E.Op==4).ToList();
        Need(start.Count<=1&&end.Count<=1&&rundown.Count<=1&&stopRundown.Count<=1&&start.Count+rundown.Count<=1&&end.Count+stopRundown.Count<=1,label+" instance not unique");
        if(start.Count==1&&end.Count==1)Need(start[0].E.Time<end[0].E.Time,label+" lifetime reversed/reused");
        if(rundown.Count==1&&end.Count==1)Need(rundown[0].E.Time<end[0].E.Time,label+" rundown follows end/reuse");
        if(start.Count==1&&stopRundown.Count==1)Need(start[0].E.Time<stopRundown[0].E.Time,label+" stop rundown precedes start/reuse");
        if(rundown.Count==1&&stopRundown.Count==1)Need(rundown[0].E.Time<stopRundown[0].E.Time,label+" rundown order reversed/reused");
        if(full)Need(start.Count==1&&end.Count==1,label+" full lifetime absent");
    }
    static string Fingerprint(uint pid,uint tid,ulong offset,uint size,uint flags) { return pid+":"+tid+":"+offset+":"+size+":"+(flags&0x43); }
    static ulong DetailNumber(string detail,string name)
    {
        var m=Regex.Match(detail,@"\b"+name+@":\s*([0-9][0-9.,]*)");Need(m.Success,"Procmon "+name+" missing");string n=m.Groups[1].Value.TrimEnd(',');
        Need(Regex.IsMatch(n,@"^(?:[0-9]+|[0-9]{1,3}(?:\.[0-9]{3})+|[0-9]{1,3}(?:,[0-9]{3})+)$"),"Ambiguous Procmon numeric format");return Num(n.Replace(".","").Replace(",",""));
    }
    // Each named I/O flag is its own comma-separated token; Synchronous Paging I/O is not Paging I/O.
    static uint ProcmonFlags(Pm p)
    {
        string text=p.Detail;const string label="I/O Flags:";int start=text.IndexOf(label,StringComparison.Ordinal);
        if(start>=0)text=text.Substring(start+label.Length);uint flags=0;
        foreach(string part in text.Split(','))switch(part.Trim()) { case "Non-cached":flags|=1;break;case "Paging I/O":flags|=2;break;case "Synchronous Paging I/O":flags|=0x40;break; }
        return flags;
    }
    static string Fingerprint(Pm p) { return Fingerprint(p.Pid,p.Tid,DetailNumber(p.Detail,"Offset"),checked((uint)DetailNumber(p.Detail,"Length")),ProcmonFlags(p)); }
    public static long[] SavedNtfs(List<Pm> pm,uint[] owners,string drive,string windows)
    {
        return pm.Where(p=>owners.Contains(p.Pid)&&p.Op=="WriteFile"&&p.Result=="SUCCESS"&&Equal(p.Path,drive+@"\$LogFile")&&(ProcmonFlags(p)&3)==3&&p.Frames.Any(f=>Equal(f,windows+@"\System32\drivers\Ntfs.sys"))&&!pm.Any(o=>owners.Contains(o.Pid)&&o.Op=="CreateFile"&&Equal(o.Path,p.Path))).Select(p=>p.Ordinal).ToArray();
    }
    // Check tokens from the controlled capture launch, including the exact backing file, without exposing unrelated commands.
    static string[] Tokens(string line) { var ms=Regex.Matches(line,@"""([^""]*)""|([^\s""]+)");Need(string.Join(" ",ms.Select(m=>m.Value))==line.Trim(),"Unsupported Procmon command quoting");return ms.Select(m=>m.Groups[1].Success?m.Groups[1].Value:m.Groups[2].Value).ToArray(); }
    public static object Read(List<Pm> pm,string kernelXml,uint[] owners,uint procmonPid,string procmonPath,string backing,string command,string device,string drive,string windows)
    {
        var writes=new List<Write>();var stacks=new List<Stack>();var lives=new List<Life>();var modules=new List<Module>();var names=new Dictionary<ulong,HashSet<string>>();var collisions=new Dictionary<string,int>();
        var headers=new List<Event>();ulong min=ulong.MaxValue,max=0;long total=0;
        foreach(var e in ReadEvents(kernelXml)) {
            total++;min=Math.Min(min,e.Time);max=Math.Max(max,e.Time);
            if(e.Guid==Header&&e.Op==0)headers.Add(e);
            if(e.Guid==FileIo&&e.Op==68) { var w=DecodeWrite(e);writes.Add(w); }
            if(e.Guid==StackWalk) { var s=DecodeStack(e);stacks.Add(s); }
            if((e.Guid==Process||e.Guid==Thread)&&e.Op>=1&&e.Op<=4) { var l=DecodeLife(e);lives.Add(l); }
            // A StackWalk payload references an original event; only Execution.ThreadID says which thread executed this record.
            if(e.Tid!=Unset) { string k=Id(e.Time,e.Tid);collisions[k]=collisions.TryGetValue(k,out int n)?n+1:1; }
            if(e.Guid==FileIo&&(e.Op==0||e.Op==32||e.Op==35||e.Op==36)) {
                Need((e.Version==2||e.Version==3)&&e.Raw==null,"Unsupported file-name layout");ulong k=Num(Field(e,"FileObject"));string name=Field(e,"FileName");
                if(!names.ContainsKey(k))names[k]=new HashSet<string>(StringComparer.OrdinalIgnoreCase);names[k].Add(name);
            }
            if(e.Guid==Image) {
                if(e.Op==33||e.Op==34) { Need(e.Version==2&&e.Raw==null&&e.Fields.Count==1,"Unsupported numeric image metadata layout");Num(Field(e,e.Op==33?"ImageBase":"HypercallPageVa")); }
                else { Need((e.Op==2||e.Op==3||e.Op==4||e.Op==10)&&e.Version==3&&e.Raw==null,"Unsupported image layout");modules.Add(new Module{E=e,Base=Num(Field(e,"ImageBase")),Size=Num(Field(e,"ImageSize")),Path=Field(e,"FileName"),Pid=U32(Field(e,"ProcessId"))}); }
            }
        }
        Need(headers.Count==1,"Unique ETW header required");var h=headers[0];
        Need(Num(Field(h,"PointerSize"))==8,"Trace pointer size must be x64");
        Need(Num(Field(h,"EventsLost"))==0&&Num(Field(h,"BuffersLost"))==0,"ETW lost events or buffers");
        var capturer=lives.Where(l=>l.E.Guid==Process&&l.Pid==procmonPid).ToList();SingleLife(capturer,"Procmon",true);
        var begin=capturer.Single(l=>l.E.Op==1);var finish=capturer.Single(l=>l.E.Op==2);
        Need(min<begin.E.Time&&max>finish.E.Time,"ETW does not enclose Procmon lifetime");
        var expected=Tokens(command);Need(expected.Length==6&&Equal(expected[0],procmonPath)&&Equal(expected[1],"/AcceptEula")&&Equal(expected[2],"/Quiet")&&Equal(expected[3],"/NoConnect")&&Equal(expected[4],"/BackingFile")&&Equal(expected[5],backing),"Expected Procmon command/backing file invalid");
        // Token count is six: executable plus three switches plus BackingFile and its value.
        Need(Tokens(Field(begin.E,"CommandLine")).SequenceEqual(expected,StringComparer.OrdinalIgnoreCase),"ETW Procmon command/backing file mismatch");
        var ns=modules.Where(m=>Equal(m.Path,Ntfs)).ToList();Need(ns.Count>0&&ns.All(m=>m.Pid==0&&(m.E.Op==3||m.E.Op==4)),"Ntfs missing or loaded/unloaded during trace");
        var ranges=ns.Select(m=>Tuple.Create(m.Base,m.Size)).Distinct().ToList();Need(ranges.Count==1&&ranges[0].Item2>0&&ranges[0].Item1<=ulong.MaxValue-ranges[0].Item2,"Ntfs image mapping not unique");
        ulong nb=ranges[0].Item1,nz=ranges[0].Item2;
        Need(!modules.Any(m=>!Equal(m.Path,Ntfs)&&(Range(m.Base,nb,nz)||Range(nb,m.Base,m.Size))),"Another image overlaps Ntfs");
        var stackIndex=stacks.GroupBy(s=>s.Time+":"+s.Pid+":"+s.Tid).ToDictionary(g=>g.Key,g=>g.ToList());
        var rows=pm.Where(p=>owners.Contains(p.Pid)&&p.Op=="WriteFile"&&Regex.IsMatch(p.Path,@"^[A-Za-z]:\\\$LogFile$")).ToList();
        var groups=new List<object>();var accepted=new List<long>();var savedNtfs=new List<long>();
        foreach(var byPath in rows.GroupBy(p=>p.Path,StringComparer.OrdinalIgnoreCase)) {
            Need(Equal(byPath.Key.Substring(0,2),drive),"Journal drive lacks its volume queries");
            Need(!pm.Any(p=>owners.Contains(p.Pid)&&p.Op=="CreateFile"&&Equal(p.Path,byPath.Key)),"Attributed direct journal open");
            string target=device+@"\$LogFile";var keys=names.Where(x=>x.Value.Contains(target)).ToList();Need(keys.Count==1&&keys[0].Value.Count==1,"File key/name not unique");ulong key=keys[0].Key;
            foreach(var byThread in byPath.GroupBy(p=>p.Tid)) {
                uint tid=byThread.Key;var ids=byThread.Select(p=>p.Pid).Distinct().ToList();Need(ids.Count==1&&tid!=Unset&&tid>0,"Procmon thread owner ambiguous");uint pid=ids[0];
                var pl=lives.Where(l=>l.E.Guid==Process&&l.Pid==pid).ToList();SingleLife(pl,"Row process");
                var tl=lives.Where(l=>l.E.Guid==Thread&&l.Tid==tid).ToList();SingleLife(tl,"Row thread");Need(tl.All(l=>l.Pid==pid),"Thread PID association mismatch");
                var group=writes.Where(w=>(w.Tid==tid||w.E.Tid==tid)&&w.Key==key).ToList();Need(group.Count>0,"Empty whole-thread write group");
                var proofs=new List<object>();
                foreach(var w in group) {
                    Need(w.Tid==tid&&w.E.Tid==tid&&(w.E.Pid==pid||w.E.Pid==Unset),"Write header/payload identity disagreement");
                    Need(collisions[Id(w.E.Time,tid)]==1,"Other event of thread has same QPC");
                    var matches=stackIndex.TryGetValue(w.E.Time+":"+pid+":"+tid,out var found)?found:new List<Stack>();Need(matches.Count==1,"Write does not have one exact StackWalk");var s=matches[0];
                    var ips=s.Frames.Where(a=>Range(a,nb,nz)).ToArray();Need(s.Frames.Length>0&&ips.Length>0,"Whole-thread write lacks Ntfs frame");
                    foreach(var life in new[]{pl,tl}) {
                        var a=life.SingleOrDefault(l=>l.E.Op==1);var b=life.SingleOrDefault(l=>l.E.Op==2);
                        Need((a==null||w.E.Time>=a.E.Time)&&(b==null||w.E.Time<=b.E.Time),"Write outside unique process/thread lifetime");
                    }
                    proofs.Add(new{event_index=w.E.Index,original_qpc=w.E.Time.ToString(CultureInfo.InvariantCulture),pid,tid,header_pid=w.E.Pid,header_tid=w.E.Tid,payload_tid=w.Tid,file_key="0x"+key.ToString("X16"),offset=w.Offset.ToString(CultureInfo.InvariantCulture),length=w.Size,flags=w.Flags&0x43,stack_event_index=s.E.Index,frame_count=s.Frames.Length,ntfs_frames=ips.Select(a=>"0x"+a.ToString("X16")).ToArray()});
                }
                var start=pm.Where(p=>p.Pid==pid&&p.Op=="Thread Create"&&Regex.IsMatch(p.Detail,@"\bThread ID:\s*"+tid+@"(?:,|$)")).ToList();
                var end=pm.Where(p=>p.Pid==pid&&p.Op=="Thread Exit"&&Regex.IsMatch(p.Detail,@"\bThread ID:\s*"+tid+@"(?:,|$)")).ToList();
                Need(start.Count<=1&&end.Count<=1,"Procmon thread reused");bool full=start.Count==1&&end.Count==1;
                if(full){Need(start[0].Ordinal<end[0].Ordinal&&byThread.All(p=>p.Ordinal>start[0].Ordinal&&p.Ordinal<end[0].Ordinal),"Procmon thread lifetime inconsistent");SingleLife(tl,"Full row thread",true);}
                var pc=byThread.GroupBy(Fingerprint).ToDictionary(g=>g.Key,g=>g.Count());var ec=group.GroupBy(w=>Fingerprint(pid,tid,w.Offset,w.Size,w.Flags)).ToDictionary(g=>g.Key,g=>g.Count());
                foreach(var f in pc)Need(ec.TryGetValue(f.Key,out int count)&&count>=f.Value&&(!full||count==f.Value),"Fingerprint counts disagree");
                if(full)Need(ec.All(f=>pc.ContainsKey(f.Key)),"Extra ETW fingerprint in full thread lifetime");
                groups.Add(new{pid,tid,process_instance_events=pl.Select(l=>new{event_index=l.E.Index,opcode=l.E.Op,qpc=l.E.Time.ToString(CultureInfo.InvariantCulture),unique_process_key=l.Key}).ToArray(),thread_instance_events=tl.Select(l=>new{event_index=l.E.Index,opcode=l.E.Op,qpc=l.E.Time.ToString(CultureInfo.InvariantCulture),pid=l.Pid,tid=l.Tid}).ToArray(),file_key="0x"+key.ToString("X16"),full_thread_lifetime=full,procmon_rows=byThread.Select(p=>new{full_ordinal=p.Ordinal,pid=p.Pid,tid=p.Tid,fingerprint=Fingerprint(p),saved_frame_count=p.Frames.Length}).ToArray(),counts=pc.Keys.Concat(ec.Keys).Distinct().Select(k=>new{fingerprint=k,procmon=pc.TryGetValue(k,out int a)?a:0,etw=ec.TryGetValue(k,out int b)?b:0}).ToArray(),events=proofs.ToArray()});
                foreach(var p in byThread.Where(p=>p.Result=="SUCCESS"&&(ProcmonFlags(p)&3)==3)) {
                    if(p.Frames.Length==0)accepted.Add(p.Ordinal);
                    else if(p.Frames.Any(path=>Equal(path,windows+@"\System32\drivers\Ntfs.sys")))savedNtfs.Add(p.Ordinal);
                }
            }
        }
        return new{accepted_full_ordinals=accepted.ToArray(),saved_ntfs_full_ordinals=savedNtfs.ToArray(),procmon_events=pm.Count,etw_events=total,events_lost=0,buffers_lost=0,ntfs_range=new{path=Ntfs,image_base="0x"+nb.ToString("X16"),image_size=nz},procmon_lifetime=new{pid=procmonPid,start_event=begin.E.Index,end_event=finish.E.Index,start_qpc=begin.E.Time.ToString(CultureInfo.InvariantCulture),end_qpc=finish.E.Time.ToString(CultureInfo.InvariantCulture)},groups=groups.ToArray()};
    }
}
