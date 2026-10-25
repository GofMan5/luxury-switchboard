package domain

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNetworkHostsAndMatching(t *testing.T) {
	for host, want := range map[string]string{
		"api.example.com":   "",
		"*.example.com":     "",
		"":                  "must be a host name",
		"example":           "must have a domain, such as example.com",
		"127.0.0.1":         "must be a name, not an IP address",
		"printer.local":     "names this machine or the local network, which plugins cannot reach",
		"localhost":         "names this machine or the local network, which plugins cannot reach",
		"API.example.com":   "must be a lowercase DNS name such as api.example.com or *.example.com",
		"example.com:443":   "must be a bare host name, without a scheme, port or path",
		"https://x.example": "must be a bare host name, without a scheme, port or path",
		"[::1]":             "must be a bare host name, without a scheme, port or path",
	} {
		if got := HostProblem(host); got != want {
			t.Errorf("HostProblem(%q) = %q, want %q", host, got, want)
		}
	}
	network := Network{Hosts: []string{"api.example.com", "*.cdn.example.net"}}
	for host, want := range map[string]bool{
		"api.example.com":      true,
		"x.api.example.com":    false,
		"a.cdn.example.net":    true,
		"a.b.cdn.example.net":  true,
		"cdn.example.net":      false,
		"evilcdn.example.net":  false,
		"api.example.com.evil": false,
	} {
		if got := network.Allows(host); got != want {
			t.Errorf("Allows(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestNetworkNeedsItsPermissionBothWays(t *testing.T) {
	pkg := validPackage()
	pkg.Script = "export default 1"
	pkg.Network = &Network{Hosts: []string{"api.example.com"}}
	if err := pkg.Validate(); err == nil || !strings.Contains(err.Error(), "net:fetch") {
		t.Fatalf("network without net:fetch: %v", err)
	}
	pkg.Permissions = append(pkg.Permissions, PermissionNetFetch)
	if err := pkg.Validate(); err != nil {
		t.Fatalf("network with net:fetch: %v", err)
	}
	pkg.Network = nil
	if err := pkg.Validate(); err == nil || !strings.Contains(err.Error(), "must list the hosts") {
		t.Fatalf("net:fetch without network: %v", err)
	}
	pkg.Network = &Network{Hosts: []string{"api.example.com", "api.example.com"}}
	pkg.Permissions = append(pkg.Permissions[:0:0], validPackage().Permissions...)
	pkg.Permissions = append(pkg.Permissions, PermissionNetFetch)
	if err := pkg.Validate(); err == nil || !strings.Contains(err.Error(), "repeats another host") {
		t.Fatalf("a repeated host: %v", err)
	}
}

func TestSecretsFollowTheirSettings(t *testing.T) {
	settings := []Setting{
		{Key: "token", Label: "Token", Type: SettingSecret},
		{Key: "mode", Label: "Mode", Type: SettingText, Default: json.RawMessage(`""`)},
	}
	if keys := SecretKeys(settings); len(keys) != 1 || keys[0] != "token" {
		t.Fatalf("secret keys %v", keys)
	}
	value := "s3cret"
	secrets, err := WithSecret(settings, nil, "token", &value)
	if err != nil || secrets["token"] != value {
		t.Fatalf("set: %v %v", secrets, err)
	}
	if _, err := WithSecret(settings, secrets, "mode", &value); err == nil {
		t.Fatal("a non-secret setting took a secret")
	}
	for _, bad := range []string{"", "line\nbreak", strings.Repeat("x", MaxSecretRunes+1)} {
		if _, err := WithSecret(settings, secrets, "token", &bad); err == nil {
			t.Fatalf("secret %q was accepted", bad[:min(len(bad), 12)])
		}
	}
	cleared, err := WithSecret(settings, secrets, "token", nil)
	if err != nil || len(cleared) != 0 || secrets["token"] != value {
		t.Fatalf("clear: %v %v (the original must stay %v)", cleared, err, secrets)
	}
	if kept := KeptSecrets(settings[1:], secrets); len(kept) != 0 {
		t.Fatalf("a secret outlived its setting: %v", kept)
	}
	if err := CheckSecrets(settings, map[string]string{"gone": "x"}); err == nil {
		t.Fatal("a stored secret without a setting was accepted")
	}
	withDefault := Setting{Key: "token", Label: "Token", Type: SettingSecret, Default: json.RawMessage(`"x"`)}
	if err := withDefault.validate("settings[0]", nil); err == nil || !strings.Contains(err.Error(), "does not apply") {
		t.Fatalf("a secret default: %v", err)
	}
}

func TestCollectionNamesAndDocuments(t *testing.T) {
	for name, want := range map[string]bool{"notes": true, "a_b-1": true, "": false, "Notes": false, "-x": false, strings.Repeat("a", 33): false} {
		if ValidCollectionName(name) != want {
			t.Errorf("ValidCollectionName(%q) != %v", name, want)
		}
	}
	for id, want := range map[string]bool{"a": true, "2026-01-01:x.y": true, "": false, "a/b": false, strings.Repeat("a", 129): false} {
		if ValidDocumentID(id) != want {
			t.Errorf("ValidDocumentID(%q) != %v", id, want)
		}
	}
	doc, err := Document(json.RawMessage(" { \"a\" : [1, 2], \"h\": \"<b>\" } "))
	if err != nil || string(doc) != `{"a":[1,2],"h":"\u003cb\u003e"}` {
		t.Fatalf("compacted %s %v", doc, err)
	}
	for _, bad := range []string{"", "null", "{", `{"a":1} {}`, `"` + strings.Repeat("x", MaxDocumentBytes) + `"`} {
		if _, err := Document(json.RawMessage(bad)); err == nil {
			t.Errorf("document %.20q was accepted", bad)
		}
	}
	if DocumentCost("ab", json.RawMessage(`{}`)) != 2+2+DocumentOverheadBytes {
		t.Fatal("document cost changed")
	}
}

func TestDevLinkRules(t *testing.T) {
	absolute := "/home/dev/plugin"
	if runtime.GOOS == "windows" {
		absolute = `C:\dev\plugin`
	}
	if !ValidDevPath(absolute) || ValidDevPath("relative/plugin") || ValidDevPath(absolute+string(filepath.Separator)+"..") || ValidDevPath(absolute+"\x00") {
		t.Fatal("dev path rules")
	}
	problem := DevProblem("one\ttwo\nthree\x00" + strings.Repeat("é", MaxDevProblemRunes))
	if !strings.HasPrefix(problem, "one two\nthree ") || !strings.HasSuffix(problem, "…") || len([]rune(problem)) != MaxDevProblemRunes {
		t.Fatalf("problem %.40q", problem)
	}
	var invalid ValidationError
	if err := (DevLink{Path: absolute}).validate(); !errors.As(err, &invalid) || invalid.Field != "dev.loadedAt" {
		t.Fatalf("a link without a load time: %v", err)
	}
}
