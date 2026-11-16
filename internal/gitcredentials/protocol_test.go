package gitcredentials_test

import (
	"encoding/json"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/gitcredentials"
)

// TestStoreWireContract proves readiness parsing and setup JSON retain the host/manager wire format at the shared protocol seam.
func TestStoreWireContract(t *testing.T) {
	for _, item := range []struct {
		wire string
		want gitcredentials.StoreState
	}{{"new\n", gitcredentials.StoreNew}, {" ready \n", gitcredentials.StoreReady}} {
		got, err := gitcredentials.ParseStoreState(item.wire)
		if err != nil || got != item.want {
			t.Fatal("valid store state rejected")
		}
	}
	if _, err := gitcredentials.ParseStoreState("unexpected"); err == nil {
		t.Fatal("invalid store state accepted")
	}
	data, err := json.Marshal(gitcredentials.SetupInput{Passphrase: "dummy", Confirmation: "confirm"})
	if err != nil || string(data) != `{"Passphrase":"dummy","Confirmation":"confirm"}` {
		t.Fatal("setup wire format changed")
	}
}
