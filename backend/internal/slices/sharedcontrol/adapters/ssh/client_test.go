package ssh

import (
	"testing"
)

func TestParserAcceptsOnlyProviderNeutralSnapshot(t *testing.T) {
	raw := []byte("{\"v\":1,\"ok\":true,\"revision\":3,\"tunnels\":[{\"name\":\"Ваш коннект\",\"state\":\"running\"}]}\n")
	snapshot, err := parseSnapshot(raw)
	if err != nil || len(snapshot.Tunnels) != 1 || snapshot.Tunnels[0].Position != 0 {
		t.Fatalf("valid snapshot rejected: %+v err=%v", snapshot, err)
	}
	leaked := []byte("{\"v\":1,\"ok\":true,\"revision\":3,\"tunnels\":[{\"name\":\"Ваш коннект\",\"state\":\"running\",\"provider\":\"hidden\"}]}\n")
	if _, err := parseSnapshot(leaked); err == nil {
		t.Fatal("provider metadata was accepted")
	}
}
