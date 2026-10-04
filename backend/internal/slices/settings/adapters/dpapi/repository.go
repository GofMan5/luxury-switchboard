package dpapi

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/appdata"
	"github.com/luxuryprivate/switchboard/backend/internal/platform/encryptedfile"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/settings/domain"
)

var magic = []byte("SWSET2\n")

type Repository struct{ path string }

type document struct {
	Version  int             `json:"version"`
	Settings domain.Settings `json:"settings"`
}

// storedDocument mirrors the persisted shape with the switch fields as
// pointers, so a file written before the switches existed is told apart from a
// file where the user turned them off: an absent field keeps the default, a
// present one wins however it reads.
type storedDocument struct {
	Version  int            `json:"version"`
	Settings storedSettings `json:"settings"`
}

type storedSettings struct {
	ListenerPort          int    `json:"listenerPort"`
	MaxRequestMiB         int    `json:"maxRequestMiB"`
	HeaderTimeoutSeconds  int    `json:"headerTimeoutSeconds"`
	StreamIdleSeconds     int    `json:"streamIdleSeconds"`
	RetryBaseMilliseconds int    `json:"retryBaseMilliseconds"`
	RetryMaxSeconds       int    `json:"retryMaxSeconds"`
	PermanentAttempts     int    `json:"permanentAttempts"`
	MaxQueued             int    `json:"maxQueued"`
	ActivityCapacity      int    `json:"activityCapacity"`
	HistoryRetentionDays  int    `json:"historyRetentionDays"`
	TunnelRetentionHours  int    `json:"tunnelRetentionHours"`
	GuardrailMode         string `json:"guardrailMode"`
	// GuardrailProviderModes rides the same document; an absent field (a file
	// from before the overrides existed) restores as no overrides, which is
	// the same behavior the domain's Normalized would give it.
	GuardrailProviderModes map[string]string `json:"guardrailProviderModes"`
	GuardrailFindings      int               `json:"guardrailFindings"`
	NotificationsEnabled   *bool             `json:"notificationsEnabled"`
	ProviderHealthEnabled  *bool             `json:"providerHealthEnabled"`
	AnimationsEnabled      *bool             `json:"animationsEnabled"`
	FailoverEnabled        *bool             `json:"failoverEnabled"`
	ChainMode              string            `json:"chainMode"`
}

func New(path string) *Repository { return &Repository{path: path} }

func DefaultPath() (string, error) {
	root, err := appdata.Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "settings.v2.dpapi"), nil
}

func (repository *Repository) Load(ctx context.Context) (domain.Settings, bool, error) {
	if err := ctx.Err(); err != nil {
		return domain.Settings{}, false, err
	}
	var value storedDocument
	found, err := encryptedfile.Load(repository.path, magic, encryptedfile.DefaultMaxPlaintext, &value)
	if err != nil {
		return domain.Settings{}, false, err
	}
	if !found {
		return domain.Settings{}, false, nil
	}
	if value.Version != 1 {
		return domain.Settings{}, false, errors.New("encrypted settings are invalid")
	}
	return value.Settings.restore(), true, nil
}

// restore maps the stored shape back onto the domain, keeping the default for
// switch fields a previous build never wrote.
func (stored storedSettings) restore() domain.Settings {
	settings := domain.Settings{
		ListenerPort:           stored.ListenerPort,
		MaxRequestMiB:          stored.MaxRequestMiB,
		HeaderTimeoutSeconds:   stored.HeaderTimeoutSeconds,
		StreamIdleSeconds:      stored.StreamIdleSeconds,
		RetryBaseMilliseconds:  stored.RetryBaseMilliseconds,
		RetryMaxSeconds:        stored.RetryMaxSeconds,
		PermanentAttempts:      stored.PermanentAttempts,
		MaxQueued:              stored.MaxQueued,
		ActivityCapacity:       stored.ActivityCapacity,
		HistoryRetentionDays:   stored.HistoryRetentionDays,
		TunnelRetentionHours:   stored.TunnelRetentionHours,
		GuardrailMode:          stored.GuardrailMode,
		GuardrailProviderModes: stored.GuardrailProviderModes,
		GuardrailFindings:      stored.GuardrailFindings,
	}

	settings.NotificationsEnabled = stored.NotificationsEnabled == nil || *stored.NotificationsEnabled
	settings.ProviderHealthEnabled = stored.ProviderHealthEnabled == nil || *stored.ProviderHealthEnabled
	settings.AnimationsEnabled = stored.AnimationsEnabled == nil || *stored.AnimationsEnabled
	settings.FailoverEnabled = stored.FailoverEnabled == nil || *stored.FailoverEnabled
	// ChainMode normalizes empty (a pre-chain file) to the default in the
	// domain; the switch itself carries the user's explicit choice.
	settings.ChainMode = stored.ChainMode

	return settings
}

func (repository *Repository) Save(ctx context.Context, settings domain.Settings) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return encryptedfile.Save(repository.path, magic, encryptedfile.DefaultMaxPlaintext, document{Version: 1, Settings: settings})
}
