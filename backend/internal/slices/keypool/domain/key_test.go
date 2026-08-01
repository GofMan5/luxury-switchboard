package domain

import (
	"strings"
	"testing"
)

func TestKeyIDUsesCanonicalCredential(t *testing.T) {
	plain, err := NewKey(Params{ProviderID: "echo", Label: "Plain", Secret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	padded, err := NewKey(Params{ProviderID: "echo", Label: "Padded", Secret: "  secret  "})
	if err != nil {
		t.Fatal(err)
	}
	if plain.ID != padded.ID || padded.Credential.Reveal() != "secret" {
		t.Fatalf("equivalent credentials produced unstable identities: plain=%s padded=%s", plain.ID, padded.ID)
	}
}

func TestKeyAcceptsUnicodeLabelAndBoundsRates(t *testing.T) {
	params := Params{ProviderID: "echo", Label: strings.Repeat("Я", 80), Secret: "secret", RPM: MaxRPM}
	if _, err := NewKey(params); err != nil {
		t.Fatalf("valid Unicode label was rejected: %v", err)
	}
	params.RPM++
	if _, err := NewKey(params); err == nil {
		t.Fatal("unbounded key RPM was accepted")
	}
}

func TestCredentialRejectsHeaderControlCharacters(t *testing.T) {
	if _, err := NewCredential("secret\r\nX-Injected: value"); err == nil {
		t.Fatal("credential containing a header injection was accepted")
	}
}
