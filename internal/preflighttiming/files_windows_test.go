package preflighttiming_test

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/preflighttiming"
)

// TestWindowsHeldObservationMatchesReplaceContract proves a held-file update
// either publishes a complete returned observation or retains entered-open data,
// matching an independent native replace probe, at the public observer seam.
func TestWindowsHeldObservationMatchesReplaceContract(t *testing.T) {
	replaceAllowed := windowsHeldReplaceSucceeds(t)
	directory, ledger := observedChannel(t)
	_, token, err := ledger.Launch("suite", "inspection")
	if err != nil {
		t.Fatal(err)
	}
	observer := observerFor(directory, token)
	var held syscall.Handle
	value, err := preflighttiming.Observe(observer, preflighttiming.Preflight, func() (int, error) {
		name, err := syscall.UTF16PtrFromString(filepath.Join(directory, "inv-"+token+".json"))
		if err != nil {
			t.Fatal(err)
		}
		held, err = syscall.CreateFile(name, syscall.GENERIC_READ, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, 0, 0)
		if err != nil {
			t.Fatalf("hold observation without FILE_SHARE_DELETE: %v", err)
		}
		return 73, nil
	})
	defer func() {
		if held != syscall.InvalidHandle {
			_ = syscall.CloseHandle(held)
		}
	}()
	observer.Returned()
	if value != 73 || err != nil {
		t.Fatalf("replace failure changed call: %d %v", value, err)
	}
	journal, err := preflighttiming.Read(directory, strings.Repeat("a", 40))
	if err != nil || len(journal.Entries) != 1 || journal.Entries[0].Invocation == nil {
		t.Fatalf("held observation unreadable: %+v %v", journal, err)
	}
	phase := journal.Entries[0].Invocation.Preflight
	if replaceAllowed {
		if phase == nil || phase.State != preflighttiming.StateClosed || phase.Outcome != preflighttiming.OutcomePass || phase.Seconds == nil || !journal.Entries[0].Invocation.Returned {
			t.Fatalf("supported held replace lost completion: %+v", journal.Entries[0].Invocation)
		}
	} else if phase == nil || phase.State != preflighttiming.StateOpen || phase.Seconds != nil || journal.Entries[0].Invocation.Returned {
		t.Fatalf("failed held replace claimed completion: %+v", journal.Entries[0].Invocation)
	}
	if err := syscall.CloseHandle(held); err != nil {
		t.Fatal(err)
	}
	held = syscall.InvalidHandle
	_, next, err := ledger.Launch("suite", "inspection")
	if err != nil {
		t.Fatal(err)
	}
	observer = observerFor(directory, next)
	preflighttiming.Observe(observer, preflighttiming.Preflight, func() (int, error) { return 73, nil })
	observer.Returned()
	journal, err = preflighttiming.Read(directory, strings.Repeat("a", 40))
	if err != nil || len(journal.Entries) != 2 || journal.Entries[1].Invocation.Preflight.State != preflighttiming.StateClosed || !journal.Entries[1].Invocation.Returned {
		t.Fatalf("normal replace did not recover: %+v %v", journal, err)
	}
}

// directoryJunction creates an ordinary-user mount-point reparse entry only
// between test-owned directories using the Windows filesystem control API.
func directoryJunction(t *testing.T, link, target string) {
	t.Helper()
	if err := os.Mkdir(link, 0700); err != nil {
		t.Fatal(err)
	}
	substitute, err := syscall.UTF16FromString(`\??\` + target)
	if err != nil {
		t.Fatal(err)
	}
	printName, err := syscall.UTF16FromString(target)
	if err != nil {
		t.Fatal(err)
	}
	// REPARSE_DATA_BUFFER: 8-byte header, 8-byte mount-point offsets/lengths,
	// then two NUL-terminated UTF-16 names. Offsets are relative to PathBuffer.
	buffer := make([]byte, 16+2*(len(substitute)+len(printName)))
	binary.LittleEndian.PutUint32(buffer[0:4], 0xa0000003) // IO_REPARSE_TAG_MOUNT_POINT
	binary.LittleEndian.PutUint16(buffer[4:6], uint16(len(buffer)-8))
	binary.LittleEndian.PutUint16(buffer[10:12], uint16(2*(len(substitute)-1)))
	binary.LittleEndian.PutUint16(buffer[12:14], uint16(2*len(substitute)))
	binary.LittleEndian.PutUint16(buffer[14:16], uint16(2*(len(printName)-1)))
	for index, value := range append(substitute, printName...) {
		binary.LittleEndian.PutUint16(buffer[16+2*index:], value)
	}
	name, err := syscall.UTF16PtrFromString(link)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := syscall.CreateFile(name, syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_OPEN_REPARSE_POINT|syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		t.Fatalf("open private junction entry: %v", err)
	}
	defer syscall.CloseHandle(handle)
	var returned uint32
	if err := syscall.DeviceIoControl(handle, 0x900a4, &buffer[0], uint32(len(buffer)), nil, 0, &returned, nil); err != nil {
		t.Fatalf("create private directory junction (requires a filesystem supporting mount-point reparse data): %v", err)
	}
	t.Cleanup(func() {
		if err := os.Remove(link); err != nil {
			t.Error(err)
		}
	})
}

// TestWindowsJunctionChannelIsUnavailable proves an actual directory junction,
// without symlink privilege or a conditional skip, is refused before reads or
// writes at the public reader, ledger and producer seams.
func TestWindowsJunctionChannelIsUnavailable(t *testing.T) {
	directory, ledger := observedChannel(t)
	_, token, err := ledger.Launch("suite", "inspection")
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "channel-junction")
	directoryJunction(t, link, directory)
	if _, err := preflighttiming.Read(link, strings.Repeat("a", 40)); !errors.Is(err, preflighttiming.ErrUnavailable) {
		t.Fatalf("junction reader accepted: %v", err)
	}
	if _, err := preflighttiming.OpenLedger(link, strings.Repeat("a", 40)); !errors.Is(err, preflighttiming.ErrUnavailable) {
		t.Fatalf("junction ledger accepted: %v", err)
	}
	if observerFor(link, token) != nil {
		t.Fatal("junction producer activated")
	}
	if _, err := os.Stat(filepath.Join(directory, "inv-"+token+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("junction observer wrote through reparse entry: %v", err)
	}
}

// windowsHeldReplaceSucceeds probes os.Root.Rename independently with the same
// sharing flags: Go 1.27 can use POSIX-style replace on filesystems supporting it.
func windowsHeldReplaceSucceeds(t *testing.T) bool {
	t.Helper()
	directory := t.TempDir()
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "source"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	name, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		t.Fatal(err)
	}
	held, err := syscall.CreateFile(name, syscall.GENERIC_READ, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.CloseHandle(held)
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	err = root.Rename("source", "target")
	// Win32 ERROR_SHARING_VIOLATION is not exported by package syscall.
	const sharingViolation = syscall.Errno(32)
	if err != nil && !errors.Is(err, sharingViolation) && !errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
		t.Fatalf("unexpected held-file replace error: %v", err)
	}
	t.Logf("native held-file replace allowed: %v", err == nil)
	return err == nil
}
