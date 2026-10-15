//go:build linux

package cli_test

import (
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestAzureDevOpsTerminalHidesToken proves the real terminal adapter disables echo for PAT input and restores terminal settings through the host CLI and a Linux pseudo-terminal.
func TestAzureDevOpsTerminalHidesToken(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		status      int
	}{
		{"success", "terminal-pat-sentinel\n", 0},
		{"cancel", "terminal-pat-sentinel\x03", 1},
		{"empty", "\n", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
				t.Fatal(err)
			}
			n, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
			if err != nil {
				t.Fatal(err)
			}
			slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|unix.O_NOCTTY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer slave.Close()
			original, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
			if err != nil {
				t.Fatal(err)
			}
			f := sandboxStateFixture(t)
			f.host.Stdin = slave
			f.host.IsInputTerminal = nil
			f.host.IsOutputTerminal = func(io.Writer) bool { return false }
			f.responses[integrationProbe] = response{}
			f.responses["exec --user=1000:1000 --env HOME=/home/agent -i "+shellContainer+" "+integrationManager+" integrations login azdo --organization https://dev.azure.com/contoso"] = response{}
			echoDisabled := make(chan bool, 1)
			go func() {
				hidden := false
				deadline := time.Now().Add(time.Second)
				for time.Now().Before(deadline) {
					state, e := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
					if e == nil && state.Lflag&unix.ECHO == 0 {
						hidden = true
						break
					}
					time.Sleep(time.Millisecond)
				}
				_, _ = master.Write([]byte(tc.input))
				echoDisabled <- hidden
			}()
			status, output, _ := f.run(t, "integrations", "login", "agent01", "azdo", "--organization", "https://dev.azure.com/contoso")
			if !<-echoDisabled {
				t.Error("terminal echo was never disabled")
			}
			restored, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
			if err != nil {
				t.Fatal(err)
			}
			if *restored != *original {
				t.Error("terminal settings were not restored")
			}
			if status != tc.status || !strings.Contains(output, "Token:") || strings.Contains(output, "terminal-pat-sentinel") {
				t.Fatalf("status=%d output=%s", status, output)
			}

		})
	}
}
