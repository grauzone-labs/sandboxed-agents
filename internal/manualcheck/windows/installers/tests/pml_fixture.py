"""Synthetic Process Monitor backing log (PML) builder for the pipe-query reader tests.

The layout follows the third-party format notes of procmon-parser (docs/PML Format.md) for the
header, the event header, the event offsets array, and the processes array, and the detail bytes
the 2026-10-23 probe of a real Process Monitor 4.12 log confirmed for a named-pipe
QueryInformationFile event: the sub-operation byte, the eight-byte information length at offset
16, the eight-byte information class at offset 24, and the path at offset 64 as a flags word
(bit 15 set for ASCII, the low bits the character count), two bytes of padding, and the
characters. Every builder parameter has the real log's value by default, so a test changes one
thing at a time; the bytes are never acceptance evidence.
"""
import struct
from datetime import datetime, timezone

HEADER_SIZE = 0x3A8
EVENT_HEADER_SIZE = 0x34
PROCESS_RECORD_SIZE = 0x6C
EPOCH = datetime(1601, 1, 1, tzinfo=timezone.utc)
CLASS_PROCESS, CLASS_REGISTRY, CLASS_FILE, CLASS_IPC = 1, 2, 3, 6
OPERATIONS = {'CreateFile': 20, 'CreatePipe': 21, 'IRP_MJ_CLOSE': 22, 'ReadFile': 23, 'WriteFile': 24, '<Unknown>': 25, 'QueryInformationFile': 25,
              'QueryBasicInformationFile': 25, 'SetPipeInformation': 26, 'SetEAFile': 28, 'FileSystemControl': 33, 'CloseFile': 38, 'QuerySecurityFile': 40,
              'RegOpenKey': 0, 'RegSetValue': 5, 'Process Create': 1, 'Process Start': 2, 'Process Exit': 3}
RESULTS = {'SUCCESS': 0, 'PIPE CONNECTED': 0xC00000B2, 'INSTANCE NOT AVAILABLE': 0xC00000AB, 'PIPE BROKEN': 0xC000014B,
           'ACCESS DENIED': 0xC0000022, 'NAME NOT FOUND': 0xC0000034, 'FAST IO DISALLOWED': 0xC0000001, 'BUFFER OVERFLOW': 0x80000005}
FILE_PIPE_INFORMATION, FILE_PIPE_LOCAL_INFORMATION = 23, 24
PIPE_INFORMATION_LENGTHS = {FILE_PIPE_INFORMATION: 8, FILE_PIPE_LOCAL_INFORMATION: 40}


def filetime(moment):
    """filetime converts an aware datetime to 100-nanosecond intervals since 1601-01-01 UTC."""
    delta = moment.astimezone(timezone.utc) - EPOCH
    return delta.days * 864000000000 + delta.seconds * 10000000 + delta.microseconds * 10


def time_of_day(moment):
    """time_of_day renders a moment the way Process Monitor's CSV shows it under an invariant culture."""
    return f"{moment:%H:%M:%S}.{moment.microsecond * 10:07d}"


def event_class(operation, path):
    """event_class returns the Process Monitor event class a row belongs to."""
    if operation.startswith('Process ') or operation.startswith('Thread '):
        return CLASS_PROCESS
    if operation.startswith('Reg'):
        return CLASS_REGISTRY
    if path.startswith('\\Device\\NamedPipe'):
        return CLASS_IPC
    return CLASS_FILE


def path_bytes(path, ascii_path=True):
    """path_bytes encodes a detail string as the flags word, padding, and characters."""
    if ascii_path:
        return struct.pack('<HH', 0x8000 | len(path), 0) + path.encode('ascii')
    return struct.pack('<HH', len(path), 0) + path.encode('utf-16-le')


def pipe_query_details(path, info_class, length=None, sub_operation=None, ascii_path=True, irp_flags=0x60870, trailing=4):
    """pipe_query_details builds the detail block of a named-pipe QueryInformationFile event."""
    if length is None:
        length = PIPE_INFORMATION_LENGTHS[info_class]
    if sub_operation is None:
        sub_operation = info_class
    block = bytearray(struct.pack('<B7x', sub_operation))
    block += struct.pack('<II', irp_flags, 9)
    block += struct.pack('<QQ', length, info_class)
    block += struct.pack('<Q', 0xFFFFE58987CF5D30)
    block += bytes(64 - len(block))
    block += path_bytes(path, ascii_path) + bytes(trailing)
    return bytes(block)


def plain_details(path):
    """plain_details builds a detail block that carries only the path, for events the reader skips."""
    return bytes(8) + bytes(56) + path_bytes(path)


def event_bytes(process_index, tid, cls, operation, moment, status, details, depth=2, duration=5):
    """event_bytes builds one event: header, stack frames, and detail block."""
    frames = b''.join(struct.pack('<Q', 0xFFFFF80042E79B51 + i) for i in range(depth))
    header = struct.pack('<IIIH6xQqIHHII', process_index, tid, cls, operation, duration, filetime(moment), status, depth, 0, len(details), 0)
    assert len(header) == EVENT_HEADER_SIZE
    return header + frames + details


def process_records(processes):
    """process_records builds the processes array for (index, pid, parent pid) triples."""
    count = len(processes)
    array = bytearray(struct.pack('<I', count))
    array += b''.join(struct.pack('<I', index) for index, _, _ in processes)
    records_start = 4 + 8 * count
    array += b''.join(struct.pack('<I', records_start + i * PROCESS_RECORD_SIZE) for i in range(count))
    for index, pid, parent in processes:
        record = bytearray(PROCESS_RECORD_SIZE)
        struct.pack_into('<IIII', record, 0, index, pid, parent, 0)
        struct.pack_into('<I', record, 0x34, 1)
        array += record
    return bytes(array)


def build(events, processes, signature=b'PML_', version=9, is64=1, event_count=None, offsets_entry_size=5, truncate_at=None, offsets_delta=0):
    """build assembles a PML file from event byte strings and a processes array."""
    header = bytearray(HEADER_SIZE)
    header[0:4] = signature
    struct.pack_into('<II', header, 4, version, is64)
    for offset, text in ((0xC, 'synthetic'), (0x2C, 'C:\\Windows')):
        encoded = text.encode('utf-16-le')
        header[offset:offset + len(encoded)] = encoded  # same length on both sides, so the header keeps its size
    body = bytearray()
    offsets = []
    for event in events:
        offsets.append(HEADER_SIZE + len(body))
        body += event
    offsets_array = HEADER_SIZE + len(body)
    for offset in offsets:
        body += struct.pack('<I', offset + offsets_delta) + bytes(offsets_entry_size - 4)
    processes_array = HEADER_SIZE + len(body)
    body += process_records(processes)
    strings_array = HEADER_SIZE + len(body)
    body += struct.pack('<I', 0)
    struct.pack_into('<I', header, 0x234, len(events) if event_count is None else event_count)
    struct.pack_into('<QQQQ', header, 0x240, HEADER_SIZE, offsets_array, processes_array, strings_array)
    struct.pack_into('<Q', header, 0x398, HEADER_SIZE)
    data = bytes(header) + bytes(body)
    return data[:truncate_at] if truncate_at is not None else data


def rows_to_pml(rows, moments, details=None, event_classes=None, event_process_indexes=None, event_tids=None, statuses=None, operations=None, parents=None, **options):
    """rows_to_pml builds a PML whose events mirror CSV rows in order. Process, thread, class,
    operation, time, and result come from the row and its moment; details come from the details
    map by zero-based row index, or carry only the path. The override maps replace one field of one
    event so that a test can make exactly one identity disagree with the CSV."""
    details, event_classes, event_process_indexes = details or {}, event_classes or {}, event_process_indexes or {}
    event_tids, statuses, operations, parents = event_tids or {}, statuses or {}, operations or {}, parents or {}
    pids = []
    for row in rows:
        if int(row['PID']) not in pids:
            pids.append(int(row['PID']))
    process_indexes = {pid: 900 + i for i, pid in enumerate(pids)}
    events = []
    for i, (row, moment) in enumerate(zip(rows, moments)):
        pid = int(row['PID'])
        events.append(event_bytes(event_process_indexes.get(i, process_indexes[pid]), event_tids.get(i, int(row['TID'])),
                                  event_classes.get(i, event_class(row['Operation'], row['Path'])), operations.get(i, OPERATIONS[row['Operation']]),
                                  moment, statuses.get(i, RESULTS[row['Result']]), details.get(i, plain_details(row['Path']))))
    processes = [(process_indexes[pid], pid, parents.get(pid, 4)) for pid in pids]
    return build(events, processes, **options)
