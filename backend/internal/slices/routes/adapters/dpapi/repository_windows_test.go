//go:build windows

package dpapi

import (
	"bytes"
	"context"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/routes/domain"
	"os"
	"path/filepath"
	"testing"
)

func TestRoutesRoundTripEncrypted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes.dpapi")
	repository := New(path)
	routes := []domain.Assignment{{Target: domain.TargetTunnel, PublicModel: "public-model", UpstreamModel: "private-upstream-model", ProviderID: "private-provider", Enabled: true}}
	if err := repository.Save(context.Background(), routes); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if bytes.Contains(raw, []byte("private-upstream-model")) || bytes.Contains(raw, []byte("private-provider")) {
		t.Fatal("route metadata stored as plaintext")
	}
	loaded, err := repository.Load(context.Background())
	if err != nil || len(loaded) != 1 || loaded[0].PublicModel != "public-model" {
		t.Fatalf("unexpected routes: %+v %v", loaded, err)
	}
}
