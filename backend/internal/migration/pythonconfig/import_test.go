//go:build windows

package pythonconfig

import (
	"os"
	"path/filepath"
	"testing"

	keydomain "github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
	providerdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
	routedomain "github.com/luxuryprivate/switchboard/backend/internal/slices/routes/domain"
)

func TestOversizedLegacyConfigIsRejectedBeforeDecrypting(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOCALAPPDATA", root)
	directory := filepath.Join(root, "ProviderSwitchboard")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "config.v1.dpapi")
	if err := os.WriteFile(path, make([]byte, 4*1024*1024+1), 0o600); err != nil {
		t.Fatal(err)
	}
	_, found, err := LoadDefault()
	if err == nil || found {
		t.Fatalf("oversized legacy config was accepted: found=%v err=%v", found, err)
	}
}

func TestImportedStateRejectsDuplicateIdentityRows(t *testing.T) {
	provider, err := providerdomain.New(providerdomain.Params{ID: "echo", Name: "Echo", BaseURL: "https://example.invalid/v1", AuthMode: providerdomain.AuthBearer, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	key, err := keydomain.NewKey(keydomain.Params{ProviderID: "echo", Label: "Key", Secret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	route := routedomain.Assignment{Target: routedomain.TargetTunnel, PublicModel: "public", UpstreamModel: "private", ProviderID: "echo", Enabled: true}
	for name, state := range map[string]State{
		"provider": {Providers: []providerdomain.Provider{provider, provider}, ActiveID: "echo"},
		"key":      {Providers: []providerdomain.Provider{provider}, ActiveID: "echo", Keys: []keydomain.Key{key, key}},
		"route":    {Providers: []providerdomain.Provider{provider}, ActiveID: "echo", Routes: []routedomain.Assignment{route, route}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateState(state); err == nil {
				t.Fatal("duplicate imported state was accepted")
			}
		})
	}
}
