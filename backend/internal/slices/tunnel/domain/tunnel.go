package domain

import "errors"

type State string

const (
	StateStopped  State = "stopped"
	StateStarting State = "starting"
	StateOnline   State = "online"
	StatePaused   State = "paused"
	StateError    State = "error"
)

type Config struct {
	Port             int    `json:"port"`
	Token            string `json:"-"`
	RPMPerIP         int    `json:"rpmPerIp"`
	ContextLimitKiB  int    `json:"contextLimitKiB"`
	BrandResponse    string `json:"brandResponse"`
	PublisherProfile string `json:"publisherProfile"`
}

func (config Config) Validate() error {
	if config.Port < 1 || config.Port > 65535 || len(config.Token) < 32 || config.RPMPerIP < 0 || config.ContextLimitKiB < 0 || config.ContextLimitKiB > 2*1024*1024 || len(config.BrandResponse) > 500 {
		return errors.New("invalid tunnel settings")
	}
	if config.PublisherProfile != "" {
		if _, _, err := ParsePublisherProfile(config.PublisherProfile); err != nil {
			return err
		}
	}
	return nil
}

type Snapshot struct {
	State            State  `json:"state"`
	Port             int    `json:"port"`
	Address          string `json:"address"`
	RPMPerIP         int    `json:"rpmPerIp"`
	ContextLimitKiB  int    `json:"contextLimitKiB"`
	BrandResponse    string `json:"brandResponse"`
	PublisherProfile string `json:"publisherProfile"`
	TokenConfigured  bool   `json:"tokenConfigured"`
	Error            string `json:"error,omitempty"`
}
