package domain

import (
	"strings"
	"testing"
	"time"
)

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

func TestProviderRejectsUnboundedOperationalValues(t *testing.T) {
	base := Params{ID: "provider", Name: strings.Repeat("Я", 80), BaseURL: "https://provider.example/v1", AuthMode: AuthBearer, Enabled: true}
	if _, err := New(base); err != nil {
		t.Fatalf("valid Unicode display name was rejected: %v", err)
	}
	base.RPM = MaxRPM + 1
	if _, err := New(base); err == nil {
		t.Fatal("unbounded provider RPM was accepted")
	}
	base.RPM = 0
	base.CacheTTL = 2 * time.Hour
	if _, err := New(base); err == nil {
		t.Fatal("unsupported cache TTL was accepted")
	}
}
