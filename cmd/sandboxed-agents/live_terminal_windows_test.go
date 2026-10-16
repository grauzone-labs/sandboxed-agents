//go:build windows

package main_test

import (
	"bytes"
	"os"
	"sync"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// liveTerminal runs one command whose standard streams are a pseudo console
// (ConPTY) the test owns, as a user's terminal would.
type liveTerminal struct {
	console      windows.Handle
	process      windows.Handle
	attributes   *windows.ProcThreadAttributeListContainer
	input        *os.File
	output       *os.File
	mutex        sync.Mutex
	buffer       bytes.Buffer
	done         chan struct{}
	readerDone   chan struct{}
	closeConsole sync.Once
	exit         int
}

// startLiveTerminal starts program attached to a fresh pseudo console of 120
// columns and 40 rows, so its console handles pass the executable's terminal check.
func startLiveTerminal(t *testing.T, environment []string, program string, args ...string) *liveTerminal {
	t.Helper()
	var inputRead, inputWrite, outputRead, outputWrite windows.Handle
	if err := windows.CreatePipe(&inputRead, &inputWrite, nil, 0); err != nil {
		t.Fatal(err)
	}
	if err := windows.CreatePipe(&outputRead, &outputWrite, nil, 0); err != nil {
		t.Fatal(err)
	}
	var console windows.Handle
	if err := windows.CreatePseudoConsole(windows.Coord{X: 120, Y: 40}, inputRead, outputWrite, 0, &console); err != nil {
		t.Fatalf("create pseudo console: %v", err)
	}
	// The pseudo console duplicated its ends of both pipes.
	windows.CloseHandle(inputRead)
	windows.CloseHandle(outputWrite)
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
	var information windows.ProcessInformation
	err = windows.CreateProcess(nil, commandLine, nil, nil, false, windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT, &block[0], nil, &startup.StartupInfo, &information)
	if err != nil {
		t.Fatalf("start %s in a pseudo console: %v", program, err)
	}
	windows.CloseHandle(information.Thread)
	terminal := &liveTerminal{
		console: console, process: information.Process, attributes: attributes,
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
		close(terminal.done)
	}()
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
// console flush what remains and end the output pipe for the reader.
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
	case <-deadline:
	}
	return terminal.exit, true
}

// Close ends a still running command and releases the pseudo console.
func (terminal *liveTerminal) Close() {
	select {
	case <-terminal.done:
	default:
		windows.TerminateProcess(terminal.process, 1)
		<-terminal.done
	}
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
}
