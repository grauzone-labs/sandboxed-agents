package cli

import (
	"strings"
	"testing"
)

// TestSandboxHostnameNormalization proves hostname derivation preserves the existing tailnet
// rule for mixed case, punctuation, truncation, and empty results through the naming helper.
func TestSandboxHostnameNormalization(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"agent01", "agent01"},
		{"My_Box.1", "my-box-1"},
		{"Demo..__", "demo"},
		{"A__..B", "a----b"},
		{strings.Repeat("A", 63), strings.Repeat("a", 63)},
		{strings.Repeat("B", 70), strings.Repeat("b", 63)},
		{strings.Repeat("C", 62) + "-end", strings.Repeat("c", 62)},
		{"", "sandbox"},
		{"_.-", "sandbox"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sandboxHostname(tc.name); got != tc.want {
				t.Errorf("hostname=%q want %q", got, tc.want)
			}
		})
	}
}
