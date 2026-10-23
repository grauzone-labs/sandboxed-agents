using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Text;
using System.Text.RegularExpressions;
using System.Xml;
using System.Xml.Linq;

// Raw Process Monitor backing-log reader for the named-pipe information queries that Process Monitor
// 4.12 prints as <Unknown>. It reads only the numbers the log stores for an event: the event class,
// the operation, the information class and length in the detail block, the thread, the process, the
// time, the result, and the path. It accepts nothing by itself; Read-PmlPipeQueries.ps1 joins each
// record to the CSV row and the XML event of the same ordinal and rejects any disagreement. The
// layout follows the third-party procmon-parser format notes and the bytes a real 4.12 log showed on
// 2026-10-23; anything outside that layout is a format error, never a partial result.
public static class InstallerPmlPipeQueries
{
    public class Record { public long Index,Ordinal,FileTime; public uint ProcessIndex,Pid,Tid,Status,Class; public int Operation,SubOperation; public ulong InformationClass,InformationLength; public string Path; public bool PathAscii; }
    const int HeaderSize=0x3A8, EventHeader=0x34, OffsetEntry=5, ClassIpc=6, OperationQueryInformation=25, FixedDetail=68, MaxDepth=4096, MaxDetail=1<<20, MaxProcesses=1<<16;
    static void Need(bool ok,string message) { if(!ok) throw new FormatException(message); }
    // Read returns every IPC-class QueryInformationFile event of a 64-bit version-9 log whose event
    // count equals the exported row count, after checking every event offset and header of the log.
    public static List<Record> Read(string file,long expectedEvents)
    {
        using(var stream=new FileStream(file,FileMode.Open,FileAccess.Read,FileShare.Read))
        using(var reader=new BinaryReader(stream)) {
            long length=stream.Length;Need(length>=HeaderSize,"PML shorter than its header");
            Need(Encoding.ASCII.GetString(reader.ReadBytes(4))=="PML_","PML signature absent");
            uint version=reader.ReadUInt32(),is64=reader.ReadUInt32();Need(version==9,"PML version "+version+" is not 9");Need(is64==1,"PML is not a 64-bit capture");
            stream.Position=0x234;uint count=reader.ReadUInt32();Need(count==expectedEvents,"PML holds "+count+" events, the export "+expectedEvents);
            stream.Position=0x240;long eventsStart=checked((long)reader.ReadUInt64()),offsetsArray=checked((long)reader.ReadUInt64()),processesArray=checked((long)reader.ReadUInt64());
            Need(eventsStart>=HeaderSize&&eventsStart<=length&&offsetsArray>=eventsStart&&offsetsArray+(long)count*OffsetEntry<=length&&processesArray>=HeaderSize&&processesArray<length,"PML array offsets outside the file");
            var pids=Processes(stream,reader,processesArray,length);
            // Process Monitor stores events in the order it received them, which is not the order of the
            // offsets array (a real log showed network events far behind their neighbours), so an offset
            // must only be unique and inside the events region, which the offsets array ends.
            var records=new List<Record>();var offsets=new HashSet<long>();
            for(long i=0;i<count;i++) {
                stream.Position=offsetsArray+i*OffsetEntry;long offset=reader.ReadUInt32();
                Need(offset>=eventsStart&&offset+EventHeader<=offsetsArray,"PML event offset "+i+" outside the events region");Need(offsets.Add(offset),"PML event offset "+i+" repeats another event");
                stream.Position=offset;uint processIndex=reader.ReadUInt32(),tid=reader.ReadUInt32(),cls=reader.ReadUInt32();int op=reader.ReadUInt16();
                stream.Position=offset+0x1C;long time=reader.ReadInt64();uint status=reader.ReadUInt32();int depth=reader.ReadUInt16();stream.Position=offset+0x2C;uint detailSize=reader.ReadUInt32();
                Need(cls>=1&&cls<=6,"PML event "+i+" has event class "+cls);Need(depth<=MaxDepth&&detailSize<=MaxDetail,"PML event "+i+" has an implausible stack depth or detail size");
                long detailStart=offset+EventHeader+8L*depth;Need(detailStart+detailSize<=offsetsArray,"PML event "+i+" details exceed the events region");
                if(cls!=ClassIpc||op!=OperationQueryInformation)continue;
                Need(pids.ContainsKey(processIndex),"PML event "+i+" names process index "+processIndex+" absent from the processes array");
                Need(detailSize>=FixedDetail,"PML pipe query event "+i+" details shorter than the fixed part");
                stream.Position=detailStart;byte[] detail=reader.ReadBytes((int)detailSize);Need(detail.Length==detailSize,"PML event "+i+" details truncated");
                var record=new Record{Index=i,Ordinal=i+1,ProcessIndex=processIndex,Pid=pids[processIndex],Tid=tid,Status=status,FileTime=time,Class=cls,Operation=op,SubOperation=detail[0],InformationLength=BitConverter.ToUInt64(detail,16),InformationClass=BitConverter.ToUInt64(detail,24)};
                ushort flags=BitConverter.ToUInt16(detail,64);record.PathAscii=(flags&0x8000)!=0;int characters=flags&0x7FFF;int bytes=record.PathAscii?characters:characters*2;
                Need(FixedDetail+bytes<=detailSize,"PML pipe query event "+i+" path exceeds its details");
                if(record.PathAscii) { Need(detail.Skip(FixedDetail).Take(bytes).All(b=>b<0x80),"PML pipe query event "+i+" ASCII path holds non-ASCII bytes");record.Path=Encoding.ASCII.GetString(detail,FixedDetail,bytes); }
                else record.Path=Encoding.Unicode.GetString(detail,FixedDetail,bytes);
                records.Add(record);
            }
            return records;
        }
    }
    // Processes maps every process index of the log to its process ID; offsets are relative to the array.
    static Dictionary<uint,uint> Processes(FileStream stream,BinaryReader reader,long array,long length)
    {
        stream.Position=array;uint count=reader.ReadUInt32();Need(count<=MaxProcesses&&array+4+8L*count<=length,"PML processes array implausible");
        var indexes=new uint[count];for(int i=0;i<count;i++)indexes[i]=reader.ReadUInt32();
        var offsets=new uint[count];for(int i=0;i<count;i++)offsets[i]=reader.ReadUInt32();
        var map=new Dictionary<uint,uint>();
        for(int i=0;i<count;i++) {
            long position=array+offsets[i];Need(position+8<=length,"PML process record outside the file");
            stream.Position=position;uint index=reader.ReadUInt32(),pid=reader.ReadUInt32();
            Need(index==indexes[i],"PML process record index disagrees with its array");Need(!map.ContainsKey(index),"PML duplicate process index");map[index]=pid;
        }
        return map;
    }
    static readonly Regex TimeOfDay=new Regex(@"^\s*(\d{1,2}):(\d{2}):(\d{2})(?:[.,](\d{1,7}))?\s*(?:([AaPp])\.?[Mm]\.?)?\s*$");
    // TimeOfDayTicks parses a Process Monitor Time of Day cell, in a 24-hour or AM/PM form with a
    // period or comma before up to seven fraction digits, into 100-nanosecond ticks since midnight; -1
    // when the cell is not such a time.
    public static long TimeOfDayTicks(string text)
    {
        if(text==null)return -1;var m=TimeOfDay.Match(text);if(!m.Success)return -1;
        int h=int.Parse(m.Groups[1].Value),mi=int.Parse(m.Groups[2].Value),s=int.Parse(m.Groups[3].Value);
        string fraction=m.Groups[4].Success?m.Groups[4].Value.PadRight(7,'0'):"0000000";
        if(m.Groups[5].Success) { if(h<1||h>12)return -1;h%=12;if(char.ToLowerInvariant(m.Groups[5].Value[0])=='p')h+=12; }
        if(h>23||mi>59||s>59)return -1;
        return (((long)h*3600+mi*60+s)*10000000L)+long.Parse(fraction);
    }
    // XmlEvents returns the child element values of the Procmon XML events at the given one-based
    // ordinals, streaming the file once up to the largest ordinal.
    public static Dictionary<long,Dictionary<string,string>> XmlEvents(string file,long[] ordinals)
    {
        var wanted=new HashSet<long>(ordinals);var result=new Dictionary<long,Dictionary<string,string>>();if(wanted.Count==0)return result;
        long max=wanted.Max(),ordinal=0;
        using(var r=XmlReader.Create(file,new XmlReaderSettings{DtdProcessing=DtdProcessing.Prohibit,XmlResolver=null})) {
            bool more=r.Read();
            while(more) {
                if(r.NodeType==XmlNodeType.Element&&r.LocalName=="event") {
                    ordinal++;if(ordinal>max)break;
                    if(wanted.Contains(ordinal)) {
                        XElement e;using(var sub=r.ReadSubtree())e=XElement.Load(sub);
                        var fields=new Dictionary<string,string>();
                        foreach(var child in e.Elements()) { Need(!fields.ContainsKey(child.Name.LocalName),"Duplicate XML field at event "+ordinal);fields[child.Name.LocalName]=child.Value; }
                        result[ordinal]=fields;more=r.Read();
                    } else r.Skip();
                    continue;
                }
                more=r.Read();
            }
        }
        return result;
    }
}
