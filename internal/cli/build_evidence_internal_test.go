package cli

import "testing"

// TestTimestampControlsExcludeFreshness proves the private producer-input seam recognizes timestamp controls without deriving ownership from a clock or inventory delta.
func TestTimestampControlsExcludeFreshness(t *testing.T) {
	for _, test := range []struct {
		env, args []string
		want      bool
	}{
		{nil, nil, false},
		{[]string{"UNRELATED=1"}, []string{"build", "--layers=false", "context"}, false},
		{[]string{"SOURCE_DATE_EPOCH="}, nil, true},
		{[]string{"SOURCE_DATE_EPOCH=0"}, nil, true},
		{nil, []string{"--timestamp", "0"}, true},
		{nil, []string{"--timestamp=0"}, true},
		{nil, []string{"--source-date-epoch=0"}, true},
		{nil, []string{"--rewrite-timestamp"}, true},
	} {
		if got := reproducibleBuild("linux", test.env, test.args); got != test.want {
			t.Fatalf("env=%v argv=%v got=%v", test.env, test.args, got)
		}
	}
}
