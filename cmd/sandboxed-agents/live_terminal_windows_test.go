//go:build windows

package main_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
	"golang.org/x/sys/windows"
)

// liveTerminal runs one command whose standard streams are a pseudo console
// (ConPTY) the test owns, as a user's terminal would.
type liveTerminal struct {
	console      windows.Handle
	process      windows.Handle
	job          windows.Handle
	attributes   *windows.ProcThreadAttributeListContainer
	input        *os.File
	output       *os.File
	mutex        sync.Mutex
	buffer       bytes.Buffer
	done         chan struct{}
	readerDone   chan struct{}
	closeConsole sync.Once
	closeHandles sync.Once
	exit         int
	// exited is written before done closes.
	exited time.Time
}

// startLiveTerminal starts program attached to a fresh pseudo console of 120
// columns and 40 rows, so its console handles pass the executable's terminal check.
func startLiveTerminal(t *testing.T, environment []string, program string, args ...string) *liveTerminal {
	t.Helper()
	var inputRead, inputWrite, outputRead, outputWrite windows.Handle
	var console, job windows.Handle
	var information windows.ProcessInformation
	var attributes *windows.ProcThreadAttributeListContainer
	ready := false
	defer func() {
		if ready {
			return
		}
		// A failed assignment/resume never leaves a suspended child or its handles.
		if information.Process != 0 {
			windows.TerminateProcess(information.Process, 1)
			windows.WaitForSingleObject(information.Process, windows.INFINITE)
		}
		if job != 0 {
			windows.CloseHandle(job)
		}
		if console != 0 {
			windows.ClosePseudoConsole(console)
		}
		for _, handle := range []windows.Handle{inputRead, inputWrite, outputRead, outputWrite, information.Thread, information.Process} {
			if handle != 0 {
				windows.CloseHandle(handle)
			}
		}
		if attributes != nil {
			attributes.Delete()
		}
	}()
	if err := windows.CreatePipe(&inputRead, &inputWrite, nil, 0); err != nil {
		t.Fatal(err)
	}
	if err := windows.CreatePipe(&outputRead, &outputWrite, nil, 0); err != nil {
		t.Fatal(err)
	}
	if err := windows.CreatePseudoConsole(windows.Coord{X: 120, Y: 40}, inputRead, outputWrite, 0, &console); err != nil {
		t.Fatalf("create pseudo console: %v", err)
	}
	// The pseudo console duplicated its ends of both pipes.
	windows.CloseHandle(inputRead)
	windows.CloseHandle(outputWrite)
	inputRead, outputWrite = 0, 0
	attributes, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		t.Fatal(err)
	}
	// The attribute value is the console handle itself, not a pointer to it.
	consoleValue := *(*unsafe.Pointer)(unsafe.Pointer(&console))
	if err := attributes.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, consoleValue, unsafe.Sizeof(console)); err != nil {
		t.Fatal(err)
	}
	var startup windows.StartupInfoEx
	startup.Cb = uint32(unsafe.Sizeof(startup))
	startup.ProcThreadAttributeList = attributes.List()
	// Without this flag Windows hands the command the parent's standard
	// handles, pipes under go test, and the command never sees a terminal.
	// With the flag and null handles, the command's standard handles are
	// still unset when it attaches to the pseudo console, so the attach fills
	// them with the console's own input and output handles.
	startup.Flags = windows.STARTF_USESTDHANDLES
	commandLine, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{program}, args...)))
	if err != nil {
		t.Fatal(err)
	}
	var block []uint16
	for _, entry := range environment {
		encoded, err := windows.UTF16FromString(entry)
		if err != nil {
			t.Fatal(err)
		}
		block = append(block, encoded...)
	}
	block = append(block, 0)
	job, err = windows.CreateJobObject(nil, nil)
	if err != nil {
		t.Fatalf("create terminal process job: %v", err)
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		t.Fatalf("configure terminal process job: %v", err)
	}
	// Suspend before assigning the job so no child can escape between creation
	// and containment. The private job has no breakaway flags or inherited handle.
	err = windows.CreateProcess(nil, commandLine, nil, nil, false, windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT|windows.CREATE_SUSPENDED, &block[0], nil, &startup.StartupInfo, &information)
	if err != nil {
		t.Fatalf("start %s in a pseudo console: %v", program, err)
	}
	if err := windows.AssignProcessToJobObject(job, information.Process); err != nil {
		t.Fatalf("assign terminal process job: %v", err)
	}
	if count, err := windows.ResumeThread(information.Thread); err != nil || count != 1 {
		t.Fatalf("resume terminal process: suspend count %d, error %v", count, err)
	}
	windows.CloseHandle(information.Thread)
	information.Thread = 0
	terminal := &liveTerminal{
		console: console, process: information.Process, job: job, attributes: attributes,
		input: os.NewFile(uintptr(inputWrite), "pseudo-console-input"), output: os.NewFile(uintptr(outputRead), "pseudo-console-output"),
		done: make(chan struct{}), readerDone: make(chan struct{}),
	}
	go func() {
		defer close(terminal.readerDone)
		buffer := make([]byte, 4096)
		for {
			n, err := terminal.output.Read(buffer)
			if n > 0 {
				terminal.mutex.Lock()
				terminal.buffer.Write(buffer[:n])
				terminal.mutex.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		windows.WaitForSingleObject(information.Process, windows.INFINITE)
		var code uint32
		if err := windows.GetExitCodeProcess(information.Process, &code); err != nil {
			terminal.exit = -1
		} else {
			terminal.exit = int(code)
		}
		terminal.exited = time.Now()
		close(terminal.done)
	}()
	ready = true
	return terminal
}

// Write sends keystrokes to the command as typed input.
func (terminal *liveTerminal) Write(input string) error {
	_, err := terminal.input.Write([]byte(input))
	return err
}

// Output returns everything the command has written to the console so far,
// as the pseudo console rendered it.
func (terminal *liveTerminal) Output() string {
	terminal.mutex.Lock()
	defer terminal.mutex.Unlock()
	return terminal.buffer.String()
}

// Wait returns the exit status once the command has ended within timeout and
// its output has been drained. The pseudo console renders output after the
// command exits, so Wait closes it once the command is gone, which makes the
// console flush what remains and end the output pipe for the reader. Until
// the reader has seen that end, Wait reports the command as unfinished.
func (terminal *liveTerminal) Wait(timeout time.Duration) (int, bool) {
	deadline := time.After(timeout)
	select {
	case <-terminal.done:
	case <-deadline:
		return 0, false
	}
	terminal.closeConsole.Do(func() { windows.ClosePseudoConsole(terminal.console) })
	select {
	case <-terminal.readerDone:
		return terminal.exit, true
	case <-deadline:
		return 0, false
	}
}

// completedAt reports, without blocking, when the command exited. The pseudo
// console renders the remaining output only once Wait closes it after the exit,
// so the exit is the moment the command completed.
func (terminal *liveTerminal) completedAt() (time.Time, bool) {
	select {
	case <-terminal.done:
		return terminal.exited, true
	default:
		return time.Time{}, false
	}
}

// Close ends a still running command and releases the pseudo console.
func (terminal *liveTerminal) Close() {
	terminal.closeHandles.Do(func() {
		// Kill-on-close ends every member even when the CLI itself already exited.
		windows.CloseHandle(terminal.job)
		<-terminal.done
		terminal.closeConsole.Do(func() { windows.ClosePseudoConsole(terminal.console) })
		// Closing the console ends the output pipe; give the reader a bounded
		// moment to see that before the handles go away.
		select {
		case <-terminal.readerDone:
		case <-time.After(5 * time.Second):
		}
		terminal.input.Close()
		terminal.output.Close()
		terminal.attributes.Delete()
		windows.CloseHandle(terminal.process)
	})
}

// TestLiveWindowsTerminalCloseOwnsDescendants proves closing a ConPTY ends its
// owned child tree while an independently started heartbeat survives, at the PTY seam.
func TestLiveWindowsTerminalCloseOwnsDescendants(t *testing.T) {
	directory := t.TempDir()
	marker, independent := filepath.Join(directory, "owned"), filepath.Join(directory, "independent")
	environment := append(os.Environ(), "TEST_LIVE_OWNED_FIXTURE=tree", "TEST_LIVE_OWNED_MARKER="+marker)
	for key, value := range nestedExecutables() {
		environment = append(environment, key+"="+value)
	}
	unowned := exec.Command(os.Args[0], "-test.run=^TestLiveCLIProcessFixture$")
	unowned.Env = testutil.Env(environment, map[string]string{"TEST_LIVE_OWNED_FIXTURE": "heartbeat", "TEST_LIVE_OWNED_MARKER": independent})
	if err := unowned.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.WriteFile(independent+".release", nil, 0600); _ = unowned.Wait() }()
	terminal := startLiveTerminal(t, environment, os.Args[0], "-test.run=^TestLiveCLIProcessFixture$")
	defer terminal.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		ownedData, ownedErr := os.ReadFile(marker)
		independentData, independentErr := os.ReadFile(independent)
		if ownedErr == nil && independentErr == nil && len(ownedData) > 0 && len(independentData) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("heartbeat fixture did not start: owned=%v independent=%v", ownedErr, independentErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	terminal.Close()
	first, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	otherFirst, err := os.ReadFile(independent)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	last, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	otherLast, err := os.ReadFile(independent)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(last) {
		t.Fatalf("owned descendant survived terminal close: %q -> %q", first, last)
	}
	if string(otherFirst) == string(otherLast) {
		t.Fatal("terminal close stopped an unowned process")
	}
}
