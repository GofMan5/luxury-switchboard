package domain

import "errors"

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
}

func Defaults() Settings {
	return Settings{
		ListenerPort: 8798, MaxRequestMiB: 64,
		HeaderTimeoutSeconds: 45, StreamIdleSeconds: 60,
		RetryBaseMilliseconds: 500, RetryMaxSeconds: 30,
		PermanentAttempts: 2, MaxQueued: 10_000,
		ActivityCapacity: 2_000, HistoryRetentionDays: 30,
		TunnelRetentionHours: 72,
	}
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
	default:
		return nil
	}
}
