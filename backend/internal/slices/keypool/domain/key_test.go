package domain

import "testing"

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
