package cli_test

import (
	"strings"
	"testing"
)

// scriptRecordedSwap changes only the swap field of the fake Podman inspect answer.
func scriptRecordedSwap(f *fixture, swap string) {
	key := "container inspect sandboxed-agents.default.demo"
	r := f.responses[key]
	r.Output = strings.Replace(r.Output, `"MemorySwap":8589934592,`, swap, 1)
	f.responses[key] = r
}

// TestUpChecksRecordedSwapWithMemory proves explicit memory requests validate the no-swap policy while omitted options preserve older sandboxes through the executable CLI and fake Podman seam.
func TestUpChecksRecordedSwapWithMemory(t *testing.T) {
	for _, tc := range []struct {
		name, swap, problem string
		explicit            bool
	}{
		{"no-swap", `"MemorySwap":8589934592,`, "", true},
		{"legacy-allowance", `"MemorySwap":17179869184,`, "differs", true},
		{"unlimited", `"MemorySwap":-1,`, "differs", true},
		{"missing", "", "is missing", true},
		{"null", `"MemorySwap":null,`, "is missing", true},
		{"legacy-without-options", `"MemorySwap":17179869184,`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := updateFixture(t)
			scriptRecordedSwap(f, tc.swap)
			args := []string{"up", "demo"}
			if tc.explicit {
				args = append(args, "--memory", "8g")
			}
			status, output, calls := runUpExecutable(t, f, args...)
			if tc.problem == "" {
				if status != 0 {
					t.Fatalf("status=%d output=%s", status, output)
				}
			} else if status != 1 || !strings.Contains(output, "memory-swap") || !strings.Contains(output, tc.problem) {
				t.Fatalf("status=%d output=%s; want swap %s", status, output, tc.problem)
			}
			noMutation(t, calls)
		})
	}
}

// TestUpdatePreservesRecordedSwap proves replacements carry no-swap, finite legacy, and unlimited combined limits while unreadable or invalid limits refuse before mutations through the executable CLI and fake Podman seam.
func TestUpdatePreservesRecordedSwap(t *testing.T) {
	for _, tc := range []struct {
		name, swap, want string
		valid            bool
	}{
		{"no-swap", `"MemorySwap":8589934592,`, "8589934592", true},
		{"legacy-allowance", `"MemorySwap":17179869184,`, "17179869184", true},
		{"unlimited", `"MemorySwap":-1,`, "-1", true},
		{"missing", "", "is missing", false},
		{"null", `"MemorySwap":null,`, "is missing", false},
		{"zero", `"MemorySwap":0,`, "must be -1 or at least recorded --memory", false},
		{"below-memory", `"MemorySwap":4294967296,`, "must be -1 or at least recorded --memory", false},
		{"invalid-negative", `"MemorySwap":-2,`, "must be -1 or at least recorded --memory", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := replacementFixture(t)
			scriptRecordedSwap(f, tc.swap)
			status, output, calls := runUpExecutable(t, f, "update", "demo")
			if !tc.valid {
				if status != 1 || !strings.Contains(output, "memory-swap") || !strings.Contains(output, tc.want) {
					t.Fatalf("status=%d output=%s; want %s", status, output, tc.want)
				}
				noMutation(t, calls)
				return
			}
			if status != 0 {
				t.Fatalf("status=%d output=%s calls=%v", status, output, calls)
			}
			created := false
			for _, call := range calls {
				if call[0] == "create" {
					created = true
					if !strings.Contains(strings.Join(call, " "), "--memory-swap "+tc.want+" ") {
						t.Errorf("replacement lost swap ceiling %s: %v", tc.want, call)
					}
				}
			}
			if !created {
				t.Fatal("update never created a replacement")
			}
		})
	}
}

// TestCheckReportsRecordedSwap proves diagnostics print the recorded combined memory/swap ceiling, including legacy allowances, and reject missing fields through the executable CLI and fake Podman seam.
func TestCheckReportsRecordedSwap(t *testing.T) {
	for _, tc := range []struct {
		name, swap, want string
		invalid          bool
	}{
		{"no-swap", `"MemorySwap":8589934592,`, "memory-swap 8589934592", false},
		{"legacy-allowance", `"MemorySwap":17179869184,`, "memory-swap 17179869184", false},
		{"unlimited", `"MemorySwap":-1,`, "memory-swap -1", false},
		{"missing", "", "recorded --memory-swap is missing", true},
		{"null", `"MemorySwap":null,`, "recorded --memory-swap is missing", true},
		{"zero", `"MemorySwap":0,`, "recorded --memory-swap must be -1 or at least recorded --memory", true},
		{"below-memory", `"MemorySwap":4294967296,`, "recorded --memory-swap must be -1 or at least recorded --memory", true},
		{"invalid-negative", `"MemorySwap":-2,`, "recorded --memory-swap must be -1 or at least recorded --memory", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := updateFixture(t)
			scriptRecordedSwap(f, tc.swap)
			key := "container inspect sandboxed-agents.default.demo"
			r := f.responses[key]
			r.Output = strings.Replace(r.Output, `"Running":true`, `"Running":false`, 1)
			f.responses[key] = r
			status, output, calls := runUpExecutable(t, f, "check", "demo")
			if (status != 0) != tc.invalid || !strings.Contains(output, tc.want) {
				t.Fatalf("status=%d output=%s; want %s", status, output, tc.want)
			}
			noMutation(t, calls)
		})
	}
}
