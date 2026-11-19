package liverun_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/grauzone-labs/sandboxed-agents/internal/liverun"
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// init dispatches only this test binary's native job launcher process fixture.
func init() {
	if len(os.Args) > 1 && os.Args[1] == "_fixture-job-launcher" {
		os.Exit(nativeJobLauncher(os.Args[2:]))
	}
}

// nativeJobResult retains the public command outcome at the native launcher/artifact boundary.
type nativeJobResult struct {
	Prepared, Started bool
	Error             string
}

// nativeJobLauncher calls prepare/run inside the job assigned by its parent before recording its result.
func nativeJobLauncher(args []string) int {
	root := os.Getenv("LIVERUN_FIXTURE_ROOT")
	service := liverun.New()
	service.CoordinationRoot = filepath.Join(root, "locks")
	service.MachineDisk = func(string) (string, error) { return root, nil }
	var output bytes.Buffer
	result := nativeJobResult{}
	err := service.RunDispatch(context.Background(), args, &output, &output)
	if err == nil {
		result.Prepared = true
		err = service.RunDispatch(context.Background(), []string{"run", "-run-dir", filepath.Join(root, "private")}, &output, &output)
		result.Started = err == nil
	}
	if err != nil {
		result.Error = err.Error()
	}
	data, _ := json.Marshal(result)
	temporary := filepath.Join(root, "job-result.tmp")
	if os.WriteFile(temporary, data, 0600) != nil || os.Rename(temporary, filepath.Join(root, "job-result.json")) != nil {
		return 2
	}
	for {
		time.Sleep(time.Second)
	}
}

// nativeFixtureEscapeFlags isolates the fixture from a CI parent job only through documented permitted breakaway.
func nativeFixtureEscapeFlags(t *testing.T) uint32 {
	t.Helper()
	var member int32
	procedure := windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")
	ok, _, err := procedure.Call(uintptr(windows.CurrentProcess()), 0, uintptr(unsafe.Pointer(&member)))
	if ok == 0 {
		t.Fatal(err)
	}
	if member == 0 {
		return 0
	}
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if err := windows.QueryInformationJobObject(0, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)), nil); err != nil {
		t.Fatal(err)
	}
	flags := limits.BasicLimitInformation.LimitFlags
	if flags&windows.JOB_OBJECT_LIMIT_SILENT_BREAKAWAY_OK != 0 {
		return 0
	}
	if flags&windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK != 0 {
		return windows.CREATE_BREAKAWAY_FROM_JOB
	}
	t.Skip("native CI parent job disallows fixture isolation; session-detachment refusal is required in this environment")
	return 0
}

// nativeFixtureJob creates a private non-inherited kill-on-close job with explicit breakaway policy.
func nativeFixtureJob(t *testing.T, flags uint32) windows.Handle {
	t.Helper()
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | flags
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		windows.CloseHandle(job)
		t.Fatal(err)
	}
	return job
}

// resumeNativeFixture resumes only the primary thread of the suspended launcher assigned by this test.
func resumeNativeFixture(t *testing.T, pid int) {
	t.Helper()
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID == uint32(pid) {
			thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
			if err != nil {
				t.Fatal(err)
			}
			count, err := windows.ResumeThread(thread)
			windows.CloseHandle(thread)
			if err != nil || count != 1 {
				t.Fatalf("resume count=%d err=%v", count, err)
			}
			return
		}
	}
	t.Fatal("fixture primary thread not found", err)
}

// TestWindowsRunSurvivesJobClosure proves prepare/run at the native process/job seam either escape permitted jobs or refuse before suite/build startup.
func TestWindowsRunSurvivesJobClosure(t *testing.T) {
	escape := nativeFixtureEscapeFlags(t)
	for _, mode := range []string{"no-job", "breakaway", "silent", "restrictive", "nested-restrictive"} {
		t.Run(mode, func(t *testing.T) {
			root, private, args := fixtureSetup(t)
			binary, _ := os.Executable()
			launcher := exec.Command(binary, append([]string{"_fixture-job-launcher"}, args...)...)
			launcher.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED | escape, HideWindow: true}
			var output bytes.Buffer
			launcher.Stdout, launcher.Stderr = &output, &output
			if err := launcher.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = launcher.Process.Kill() })
			process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(launcher.Process.Pid))
			if err != nil {
				t.Fatal(err)
			}
			defer windows.CloseHandle(process)
			jobs := []windows.Handle{}
			if mode != "no-job" {
				flags := uint32(0)
				if mode == "breakaway" {
					flags = windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK
				}
				if mode == "silent" {
					flags = windows.JOB_OBJECT_LIMIT_SILENT_BREAKAWAY_OK
				}
				job := nativeFixtureJob(t, flags)
				jobs = append(jobs, job)
				if err = windows.AssignProcessToJobObject(job, process); err != nil {
					t.Fatal(err)
				}
				if mode == "nested-restrictive" {
					inner := nativeFixtureJob(t, windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK)
					jobs = append(jobs, inner)
					if err = windows.AssignProcessToJobObject(inner, process); err != nil {
						t.Fatal(err)
					}
				}
			}
			defer func() {
				for _, job := range jobs {
					_ = windows.CloseHandle(job)
				}
			}()
			resumeNativeFixture(t, launcher.Process.Pid)
			waitFixture(t, filepath.Join(root, "job-result.json"))
			var result nativeJobResult
			if err = json.Unmarshal(mustReadFile(t, filepath.Join(root, "job-result.json")), &result); err != nil {
				t.Fatal(err)
			}
			refused := strings.Contains(mode, "restrictive")
			if refused {
				if result.Started || !strings.Contains(result.Error, "session-detachment") {
					t.Fatalf("unsafe job admitted: %+v output=%s", result, &output)
				}
				if _, err = os.Stat(filepath.Join(private, "started.json")); !os.IsNotExist(err) {
					t.Fatal("unsafe job started suite")
				}
				if _, err = os.Stat(filepath.Join(root, "suite-ready")); !os.IsNotExist(err) {
					t.Fatal("unsafe job reached build/test boundary")
				}
			} else if !result.Prepared || !result.Started {
				t.Fatalf("permitted detachment refused: %+v output=%s", result, &output)
			}
			for _, job := range jobs {
				_ = windows.CloseHandle(job)
			}
			jobs = nil
			if mode == "no-job" {
				_ = launcher.Process.Kill()
			}
			waited := make(chan error, 1)
			go func() { waited <- launcher.Wait() }()
			select {
			case <-waited:
			case <-time.After(5 * time.Second):
				t.Fatal("launcher did not end with session job")
			}
			if !refused {
				if err = os.WriteFile(filepath.Join(root, "release"), nil, 0600); err != nil {
					t.Fatal(err)
				}
				waitFixture(t, filepath.Join(private, "exit.json"))
				var ended struct {
					Code        int
					Interrupted bool
				}
				if err = json.Unmarshal(mustReadFile(t, filepath.Join(private, "exit.json")), &ended); err != nil {
					t.Fatal(err)
				}
				if ended.Code != 7 || ended.Interrupted {
					t.Fatalf("job closure killed detached suite: %+v", ended)
				}
			}
		})
	}
}
