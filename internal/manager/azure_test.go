package manager_test

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/manager"
)

// TestAzureDeviceLoginVerifiesAccount proves the device flow and account check run in order as agent through the manager command and injected process seam.
func TestAzureDeviceLoginVerifiesAccount(t *testing.T) {
	effects := noProcesses(t, manager.Identity{UID: 1000, GID: 1000})
	var calls [][]string
	effects.Start = func(p manager.Process) error {
		if p.Identity != (manager.Identity{UID: 1000, GID: 1000}) {
			t.Errorf("identity=%v", p.Identity)
		}
		calls = append(calls, append([]string{p.Program}, p.Args...))
		return nil
	}
	var out bytes.Buffer
	status := manager.RunWithEffects([]string{"integrations", "login", "azure", "device-code"}, &out, &out, effects)
	want := [][]string{{"az", "login", "--use-device-code"}, {"az", "account", "show"}}
	if status != 0 || !reflect.DeepEqual(calls, want) {
		t.Fatalf("status=%d calls=%q output=%s want=%q", status, calls, &out, want)
	}
}

// TestAzureBrowserLoginUsesBrowserFlow proves the browser workflow runs az login before account verification through the manager command seam.
func TestAzureBrowserLoginUsesBrowserFlow(t *testing.T) {
	effects := noProcesses(t, manager.Identity{UID: 1000, GID: 1000})
	var calls [][]string
	effects.Start = func(p manager.Process) error {
		calls = append(calls, append([]string{p.Program}, p.Args...))
		return nil
	}
	var out bytes.Buffer
	status := manager.RunWithEffects([]string{"integrations", "login", "azure", "browser", "--ssh-host", "agent01"}, &out, &out, effects)
	want := [][]string{{"az", "login"}, {"az", "account", "show"}}
	if status != 0 || !reflect.DeepEqual(calls, want) {
		t.Fatalf("status=%d calls=%q output=%s", status, calls, &out)
	}
}

// TestAzureBrowserCallbackPrintsTunnelBeforeURL proves the browser adapter derives the redirect port and shows the tunnel before the URL at the manager command seam.
func TestAzureBrowserCallbackPrintsTunnelBeforeURL(t *testing.T) {
	for _, url := range []string{"https://login.microsoftonline.com/organizations/oauth2/v2.0/authorize?redirect_uri=http%3A%2F%2Flocalhost%3A43123&state=example", "http://localhost:43123?welcome=true"} {
		var out bytes.Buffer
		status := manager.RunWithEffects([]string{"__azure-browser", "agent01.team", url}, &out, &out, noProcesses(t, manager.Identity{UID: 1000, GID: 1000}))
		tunnel := "ssh -N -L 127.0.0.1:43123:127.0.0.1:43123 -o ExitOnForwardFailure=yes agent01.team"
		if status != 0 || !strings.Contains(out.String(), tunnel) || strings.Index(out.String(), tunnel) > strings.Index(out.String(), url) {
			t.Fatalf("status=%d output=%s", status, &out)
		}
	}
}

// TestAzureLoginStopsOnFailure proves failed authentication never checks an old account and failed verification cannot report success through the manager process seam.
func TestAzureLoginStopsOnFailure(t *testing.T) {
	for _, workflow := range []string{"device-code", "browser"} {
		for _, failureAt := range []int{1, 2} {
			effects := noProcesses(t, manager.Identity{UID: 1000, GID: 1000})
			starts := 0
			effects.Start = func(manager.Process) error {
				starts++
				if starts == failureAt {
					return childExit(23)
				}
				return nil
			}
			args := []string{"integrations", "login", "azure", workflow}
			if workflow == "browser" {
				args = append(args, "--ssh-host", "agent01")
			}
			var out bytes.Buffer
			if status := manager.RunWithEffects(args, &out, &out, effects); status != 1 || starts != failureAt {
				t.Fatalf("status=%d starts=%d output=%s", status, starts, &out)
			}
		}
	}
}

// TestAzureBrowserRejectsInvalidCallback proves malformed or nonloopback redirects and unsafe aliases produce no tunnel instructions through the manager command seam.
func TestAzureBrowserRejectsInvalidCallback(t *testing.T) {
	for _, args := range [][]string{
		{"agent01", "https://login.microsoftonline.com/?redirect_uri=http%3A%2F%2Fexample.com%3A43123"},
		{"agent01", "https://login.microsoftonline.com/?redirect_uri=http%3A%2F%2Flocalhost%3A0"},
		{"agent01", "https://login.microsoftonline.com/?redirect_uri=http%3A%2F%2Flocalhost%3A65536"},
		{"agent01", "https://login.microsoftonline.com/"},
		{"agent01", "http://localhost:notaport"},
		{"agent01", "file:///etc/passwd"},
		{"agent01", "http://localhost:43123/\n"},
		{"agent01;echo bad", "http://localhost:43123"},
		{"-oProxyCommand=bad", "http://localhost:43123"},
	} {
		var out, errout bytes.Buffer
		status := manager.RunWithEffects(append([]string{"__azure-browser"}, args...), &out, &errout, noProcesses(t, manager.Identity{UID: 1000, GID: 1000}))
		if status == 0 || out.Len() != 0 {
			t.Fatalf("args=%q status=%d stdout=%s stderr=%s", args, status, &out, &errout)
		}
	}
}

// TestAzureLoginRejectsInvalidRequests proves workflow and SSH alias validation happens before external processes at the manager command seam.
func TestAzureLoginRejectsInvalidRequests(t *testing.T) {
	for _, args := range [][]string{{}, {"bogus"}, {"browser"}, {"browser", "--ssh-host", "-oBad"}, {"browser", "--ssh-host", "agent01", "extra"}, {"device-code", "--ssh-host", "agent01"}} {
		var out bytes.Buffer
		if status := manager.RunWithEffects(append([]string{"integrations", "login", "azure"}, args...), &out, &out, noProcesses(t, manager.Identity{UID: 1000, GID: 1000})); status != 2 {
			t.Fatalf("args=%q status=%d output=%s", args, status, &out)
		}
	}
}
