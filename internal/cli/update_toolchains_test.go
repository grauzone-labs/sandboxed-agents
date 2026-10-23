package cli_test

import (
	"fmt"
	"strings"
	"testing"
)

// TestUpdateExplicitToolchainsReplaceUnknownRecordedSet proves explicit sets replace unreadable recorded sets and failed starts restore the original backup unchanged through CLI creation and rollback arguments.
func TestUpdateExplicitToolchainsReplaceUnknownRecordedSet(t *testing.T) {
	for _, recorded := range []string{"", "unknown", "none,dotnet"} {
		for _, selected := range []string{"none", "native"} {
			for _, failed := range []bool{false, true} {
				t.Run(fmt.Sprintf("recorded=%s/selected=%s/failure=%t", recorded, selected, failed), func(t *testing.T) {
					f := replacementFixture(t)
					key := "container inspect sandboxed-agents.default.demo"
					r := f.responses[key]
					r.Output = strings.Replace(r.Output, `"io.sandboxed-agents.toolchains":"none"`, `"io.sandboxed-agents.toolchains":"`+recorded+`"`, 1)
					f.responses[key] = r
					tag := strings.Replace(baseTag(t), ":none-", ":"+selected+"-", 1)
					if selected != "none" {
						f.responses["image exists "+tag] = response{}
						f.responses["image inspect "+tag] = response{Output: `[{"Id":"sha256:native","Labels":{"io.sandboxed-agents.base-image":"sha256:current"}}]`}
					}
					if failed {
						f.responses["start sandboxed-agents.default.demo"] = response{Exit: 125, Output: "start failed", FailOnce: true}
						f.responses["rm --force sandboxed-agents.default.demo"] = response{}
						f.responses["rename sandboxed-agents-backup.default.demo sandboxed-agents.default.demo"] = response{}
					}
					status, output, calls := runUpExecutable(t, f, "update", "demo", "--with", selected)
					wantStatus, wantOutput := 0, "sandbox demo updated"
					if failed {
						wantStatus, wantOutput = 1, "restored sandbox demo to its previous state"
					}
					if status != wantStatus || !strings.Contains(output, wantOutput) {
						t.Fatalf("status=%d output=%s", status, output)
					}
					created := 0
					for _, call := range calls {
						if call[0] == "create" {
							created++
							if call[len(call)-1] != tag || !strings.Contains(strings.Join(call, " "), "--label io.sandboxed-agents.toolchains="+selected+" ") {
								t.Fatalf("wrong requested set: %v", call)
							}
						}
						if call[0] == "container" && len(call) > 1 && call[1] == "update" {
							t.Fatalf("modified original backup: %v", call)
						}
					}
					if created != 1 {
						t.Fatalf("created %d containers; want only one replacement", created)
					}
					if failed {
						// The restored, running sandbox restores its tool services last.
						want := "rm --force sandboxed-agents.default.demo;rename sandboxed-agents-backup.default.demo sandboxed-agents.default.demo;start sandboxed-agents.default.demo"
						if len(calls) < 4 || strings.Join(flatCalls(calls[len(calls)-4:len(calls)-1]), ";") != want || calls[len(calls)-1][len(calls[len(calls)-1])-1] != "restore" {
							t.Fatalf("backup not restored in place: %v", calls)
						}
					}
				})
			}
		}
	}
}

// TestUpdateToolchainOverridePreservesValidation proves absent overrides still require a recorded set and explicit sets bypass neither usage, ownership nor mount validation through the CLI and Podman seams.
func TestUpdateToolchainOverridePreservesValidation(t *testing.T) {
	for _, state := range []string{"no override", "invalid override", "foreign owner", "unsupported mount"} {
		t.Run(state, func(t *testing.T) {
			f := replacementFixture(t)
			key := "container inspect sandboxed-agents.default.demo"
			r := f.responses[key]
			r.Output = strings.Replace(r.Output, `"io.sandboxed-agents.toolchains":"none"`, `"io.sandboxed-agents.toolchains":"unknown"`, 1)
			args, want := []string{"update", "demo", "--with", "none"}, "invalid recorded toolchain set"
			switch state {
			case "no override":
				args = args[:2]
			case "invalid override":
				args[3] = "none,dotnet"
				want = "none"
			case "foreign owner":
				r.Output = strings.Replace(r.Output, `"io.sandboxed-agents.owner":"default"`, `"io.sandboxed-agents.owner":"foreign"`, 1)
				want = "owner conflict"
			case "unsupported mount":
				r.Output = strings.Replace(r.Output, `"Destination":"/home/agent"`, `"Destination":"/unexpected"`, 1)
				want = "unsupported recorded mount"
			}
			f.responses[key] = r
			status, output, calls := runUpExecutable(t, f, args...)
			if status == 0 || !strings.Contains(output, want) {
				t.Fatalf("status=%d output=%s", status, output)
			}
			if state == "invalid override" && len(calls) != 0 {
				t.Fatalf("Podman called on invalid supplied set: %v", calls)
			}
			for _, call := range calls {
				switch call[0] {
				case "create", "rename", "start", "stop", "rm", "build":
					t.Fatalf("changed invalid sandbox: %v", call)
				}
			}
		})
	}
}
