package domain

import (
	"errors"
	"reflect"
)

type Settings struct {
	ListenerPort          int `json:"listenerPort"`
	MaxRequestMiB         int `json:"maxRequestMiB"`
	HeaderTimeoutSeconds  int `json:"headerTimeoutSeconds"`
	StreamIdleSeconds     int `json:"streamIdleSeconds"`
	RetryBaseMilliseconds int `json:"retryBaseMilliseconds"`
	RetryMaxSeconds       int `json:"retryMaxSeconds"`
	PermanentAttempts     int `json:"permanentAttempts"`
	MaxQueued             int `json:"maxQueued"`
	ActivityCapacity      int `json:"activityCapacity"`
	HistoryRetentionDays  int `json:"historyRetentionDays"`
	TunnelRetentionHours  int `json:"tunnelRetentionHours"`
	// GuardrailMode is off, monitor or block. Settings persisted before the
	// guardrails existed carry an empty value, which reads as the default.
	GuardrailMode string `json:"guardrailMode"`
	// GuardrailProviderModes overrides the inspection mode per provider:
	// monitor or block for a provider that distrust earned, regardless of the
	// global mode. An entry equal to the global mode is redundant and reads
	// back as absent. Empty values delete the override.
	GuardrailProviderModes map[string]string `json:"guardrailProviderModes"`
	GuardrailFindings      int               `json:"guardrailFindings"`
	// NotificationsEnabled is the master switch for the notification feed and
	// its toasts. Badges and health dots stay on: they are ambient state, not
	// interruptions. Settings persisted before notifications existed read as
	// on.
	NotificationsEnabled bool `json:"notificationsEnabled"`
	// ProviderHealthEnabled runs the background reachability probe. It costs
	// one cheap catalog call per enabled provider every two minutes; off means
	// the sidebar shows configured state, not liveness.
	ProviderHealthEnabled bool `json:"providerHealthEnabled"`
	// AnimationsEnabled is the motion switch for the interface. The operating
	// system's reduced-motion preference still wins over it: a system-level
	// request is a stronger statement than an app toggle.
	AnimationsEnabled bool `json:"animationsEnabled"`
	// FailoverEnabled turns the route chains on or off. Off means terminal
	// verdicts end the request where they happened — strict routing for
	// someone who wants to know exactly which provider served them.
	FailoverEnabled bool `json:"failoverEnabled"`
	// ChainMode decides how a chain of healthy providers shares requests:
	// "failover" serves strictly by priority (the head provider until it
	// degrades), "balance" round-robins across every healthy entry, which
	// spreads a provider's shared daily quota across the whole chain.
	ChainMode string `json:"chainMode"`
}

// DefaultGuardrailMode is monitor rather than block. The detection rules match on
// shell and network idiom that a developer's own assistant produces for honest
// reasons, so blocking by default would destroy legitimate answers on day one;
// the operator turns block on for providers they do not trust.
const DefaultGuardrailMode = "monitor"

// GuardrailProviderModeCap bounds the override table. One entry per
// configured provider is the intended use; a bound keeps a corrupted file
// from growing the settings frame without a limit voting on it.
const GuardrailProviderModeCap = 128

func Defaults() Settings {
	return Settings{
		ListenerPort: 8798, MaxRequestMiB: 64,
		HeaderTimeoutSeconds: 45, StreamIdleSeconds: 60,
		RetryBaseMilliseconds: 500, RetryMaxSeconds: 30,
		PermanentAttempts: 2, MaxQueued: 10_000,
		ActivityCapacity: 2_000, HistoryRetentionDays: 30,
		TunnelRetentionHours:   72,
		GuardrailMode:          DefaultGuardrailMode,
		GuardrailProviderModes: map[string]string{},
		GuardrailFindings:      500,
		NotificationsEnabled:   true,
		ProviderHealthEnabled:  true,
		AnimationsEnabled:      true,
		FailoverEnabled:        true,
		ChainMode:              DefaultChainMode,
	}
}

// DefaultChainMode is balance rather than failover: the chains exist to absorb
// dying providers, and a healthy chain that also shares the load turns two
// free resellers into twice the daily quota — the reason most operators build
// one. Strict-priority routing stays one toggle away.
const DefaultChainMode = "balance"

// Equal answers whether two settings are the same value, map included. The
// struct stopped being comparable when it gained a map field; tests and the
// settings service compare values, not identities.
func (settings Settings) Equal(other Settings) bool {
	return reflect.DeepEqual(settings, other)
}

// Normalized fills in values that predate a field, so settings written by an
// older build load instead of failing validation.
func (settings Settings) Normalized() Settings {
	if settings.GuardrailMode == "" {
		settings.GuardrailMode = DefaultGuardrailMode
	}
	if settings.GuardrailFindings == 0 {
		settings.GuardrailFindings = Defaults().GuardrailFindings
	}
	if settings.ChainMode == "" {
		settings.ChainMode = DefaultChainMode
	}
	if settings.GuardrailProviderModes == nil {
		settings.GuardrailProviderModes = map[string]string{}
	}
	// A global "off" overrides nothing per provider: inspection disabled is
	// disabled everywhere, and a table that would silently re-enable it under
	// some providers is a settings file that lies about what it does.
	if settings.GuardrailMode == "off" {
		settings.GuardrailProviderModes = map[string]string{}
	}
	return settings
}

func (settings Settings) Validate() error {
	switch {
	case settings.ListenerPort < 1 || settings.ListenerPort > 65535:
		return errors.New("listener port is out of range")
	case settings.MaxRequestMiB < 1 || settings.MaxRequestMiB > 256:
		return errors.New("maximum request size is out of range")
	case settings.HeaderTimeoutSeconds < 5 || settings.HeaderTimeoutSeconds > 300:
		return errors.New("header timeout is out of range")
	case settings.StreamIdleSeconds < 15 || settings.StreamIdleSeconds > 900:
		return errors.New("stream idle timeout is out of range")
	case settings.RetryBaseMilliseconds < 50 || settings.RetryBaseMilliseconds > 10_000:
		return errors.New("retry base is out of range")
	case settings.RetryMaxSeconds < 1 || settings.RetryMaxSeconds > 120:
		return errors.New("retry maximum is out of range")
	case settings.PermanentAttempts < 1 || settings.PermanentAttempts > 3:
		return errors.New("permanent attempts are out of range")
	case settings.MaxQueued < 100 || settings.MaxQueued > 100_000:
		return errors.New("maximum queue size is out of range")
	case settings.ActivityCapacity < 100 || settings.ActivityCapacity > 20_000:
		return errors.New("activity capacity is out of range")
	case settings.HistoryRetentionDays < 1 || settings.HistoryRetentionDays > 365:
		return errors.New("history retention is out of range")
	case settings.TunnelRetentionHours < 24 || settings.TunnelRetentionHours > 720:
		return errors.New("tunnel retention is out of range")
	case settings.GuardrailMode != "off" && settings.GuardrailMode != "monitor" && settings.GuardrailMode != "block":
		return errors.New("guardrail mode must be off, monitor or block")
	case settings.GuardrailFindings < 50 || settings.GuardrailFindings > 5_000:
		return errors.New("guardrail finding capacity is out of range")
	case settings.ChainMode != "failover" && settings.ChainMode != "balance":
		return errors.New("chain mode must be failover or balance")
	case len(settings.GuardrailProviderModes) > GuardrailProviderModeCap:
		return errors.New("too many guardrail provider overrides")
	default:
		for provider, mode := range settings.GuardrailProviderModes {
			if provider == "" {
				return errors.New("guardrail provider override needs a provider")
			}
			if mode != "monitor" && mode != "block" {
				return errors.New("guardrail provider override must be monitor or block")
			}
		}
		return nil
	}
}
