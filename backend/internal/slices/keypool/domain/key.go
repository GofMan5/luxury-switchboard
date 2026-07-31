package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
)

type Credential struct {
	value string
}

func NewCredential(value string) (Credential, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 8*1024 {
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
	if providerID == "" || label == "" || len(label) > 80 || params.RPM < 0 {
		return Key{}, errors.New("invalid key settings")
	}
	credential, err := NewCredential(params.Secret)
	if err != nil {
		return Key{}, err
	}
	digest := sha256.Sum256([]byte(providerID + "\x00" + credential.Reveal()))
	proxyURL := strings.TrimSpace(params.ProxyURL)
	if proxyURL != "" {
		parsed, err := url.Parse(proxyURL)
		if err != nil || parsed.Host == "" || parsed.Fragment != "" ||
			(parsed.Scheme != "http" && parsed.Scheme != "https" && parsed.Scheme != "socks5" && parsed.Scheme != "socks5h") {
			return Key{}, errors.New("invalid proxy URL")
		}
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
}
