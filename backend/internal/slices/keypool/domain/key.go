package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"unicode/utf8"
)

const MaxRPM = 1_000_000

type Credential struct {
	value string
}

func NewCredential(value string) (Credential, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 8*1024 || strings.ContainsAny(value, "\r\n\x00") {
		return Credential{}, errors.New("invalid credential")
	}
	return Credential{value: value}, nil
}

func (credential Credential) Reveal() string {
	return credential.value
}

type Key struct {
	ID         string
	ProviderID string
	Label      string
	Credential Credential
	Priority   int
	RPM        int
	Pinned     bool
	ProxyURL   string
}

type Params struct {
	ProviderID string
	Label      string
	Secret     string
	Priority   int
	RPM        int
	Pinned     bool
	ProxyURL   string
}

func NewKey(params Params) (Key, error) {
	providerID := strings.TrimSpace(params.ProviderID)
	label := strings.TrimSpace(params.Label)
	if providerID == "" || len(providerID) > 64 || label == "" || utf8.RuneCountInString(label) > 80 ||
		params.Priority < 0 || params.Priority > MaxRPM || params.RPM < 0 || params.RPM > MaxRPM ||
		strings.ContainsAny(providerID+label, "\r\n\x00") {
		return Key{}, errors.New("invalid key settings")
	}
	credential, err := NewCredential(params.Secret)
	if err != nil {
		return Key{}, err
	}
	digest := sha256.Sum256([]byte(providerID + "\x00" + credential.Reveal()))
	proxyURL := strings.TrimSpace(params.ProxyURL)
	if proxyURL != "" {
		if len(proxyURL) > 8*1024 {
			return Key{}, errors.New("invalid proxy URL")
		}
		parsed, err := url.Parse(proxyURL)
		if err != nil || parsed.Host == "" || parsed.Fragment != "" {
			return Key{}, errors.New("invalid proxy URL")
		}
		parsed.Scheme = strings.ToLower(parsed.Scheme)
		if !ProxySchemeAllowed(parsed.Scheme) {
			return Key{}, errors.New("invalid proxy URL")
		}
		proxyURL = parsed.String()
	}
	return Key{
		ID:         "key_" + hex.EncodeToString(digest[:8]),
		ProviderID: providerID,
		Label:      label,
		Credential: credential,
		Priority:   params.Priority,
		RPM:        params.RPM,
		Pinned:     params.Pinned,
		ProxyURL:   proxyURL,
	}, nil
}

type PublicKey struct {
	ID              string `json:"id"`
	ProviderID      string `json:"providerId"`
	Label           string `json:"label"`
	Priority        int    `json:"priority"`
	RPM             int    `json:"rpm"`
	Pinned          bool   `json:"pinned"`
	ProxyConfigured bool   `json:"proxyConfigured"`
	CooldownMS      int64  `json:"cooldownMs"`
	BlockedModels   int    `json:"blockedModels"`
	Retries429      int    `json:"retries429"`
	StartsInWindow  int    `json:"startsInWindow"`
	// AuthStreak is how many authentication refusals this key answered in a
	// row. Three or more reads as a dead credential: an auth verdict never
	// resolves on its own, so the operator should revoke and replace rather
	// than wait.
	AuthStreak int `json:"authStreak"`
	// LastOutcome is the outcome kind of the key's last finished attempt, so
	// the operator sees WHY a key sits where it sits.
	LastOutcome string `json:"lastOutcome"`
}
