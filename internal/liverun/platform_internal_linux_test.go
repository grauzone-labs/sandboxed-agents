package liverun

import (
	"testing"

	"golang.org/x/sys/unix"
)

// TestAvailableBytesUsesFragmentSize proves the Linux free-space reader counts
// available blocks in fragment-size units, falling back to the block size when
// the filesystem reports no fragment size. It exercises the statfs result seam.
func TestAvailableBytesUsesFragmentSize(t *testing.T) {
	for _, tc := range []struct {
		name string
		stat unix.Statfs_t
		want uint64
	}{
		{"fragment", unix.Statfs_t{Bavail: 100000, Bsize: 4096, Frsize: 512}, 51200000},
		{"no fragment", unix.Statfs_t{Bavail: 100000, Bsize: 4096}, 409600000},
	} {
		if got := availableBytes(tc.stat); got != tc.want {
			t.Errorf("%s: availableBytes = %d; want %d", tc.name, got, tc.want)
		}
	}
}
