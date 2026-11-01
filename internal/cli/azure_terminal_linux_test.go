package cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestAzureBrowserRestoresTerminalAfterFailure proves cancelling a raw-mode Podman client restores the host terminal through the CLI and a real Linux pseudo-terminal.
func TestAzureBrowserRestoresTerminalAfterFailure(t *testing.T) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	number, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|unix.O_NOCTTY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer slave.Close()
	previous, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	ready, resume := filepath.Join(dir, "ready"), filepath.Join(dir, "resume")
	f := azureBrowserFixture(t, response{Output: "\x1eSANDBOXED_AGENTS_AZURE_BROWSER invalid-json\n", ReadyFile: ready, WaitFor: resume, DelayAfterOutputMS: 10000})
	f.host.Stdin = slave
	changed := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(ready); err == nil {
				break
			}
			if time.Now().After(deadline) {
				changed <- fmt.Errorf("Podman did not start")
				return
			}
			time.Sleep(time.Millisecond)
		}
		raw := *previous
		raw.Lflag &^= unix.ICANON | unix.ECHO | unix.ISIG
		if err := unix.IoctlSetTermios(int(slave.Fd()), unix.TCSETS, &raw); err != nil {
			changed <- err
			return
		}
		changed <- os.WriteFile(resume, nil, 0600)
	}()
	status, output, _ := f.run(t, "integrations", "login", "agent01", "azure", "browser")
	if err := <-changed; err != nil {
		t.Fatal(err)
	}
	restored, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	if status != 1 || *restored != *previous {
		t.Fatalf("status=%d terminal restored=%v output=%q", status, *restored == *previous, output)
	}
}
