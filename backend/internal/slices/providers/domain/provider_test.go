package domain

import "testing"

func TestRemoteHTTPProviderIsRejectedButLoopbackIsAllowed(t *testing.T) {
	base := Params{ID: "provider", Name: "Provider", AuthMode: AuthBearer, Enabled: true}
	base.BaseURL = "http://provider.example/v1"
	if _, err := New(base); err == nil {
		t.Fatal("remote plaintext provider was accepted")
	}
	base.BaseURL = "http://127.0.0.1:8799"
	if _, err := New(base); err != nil {
		t.Fatalf("loopback provider was rejected: %v", err)
	}
}
