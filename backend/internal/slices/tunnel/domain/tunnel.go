package domain

import (
	"errors"
	"strings"
	"unicode/utf8"
)

type State string

const (
	StateStopped  State = "stopped"
	// StateInstalling is the one-time connector download: a first start on a
	// fresh machine spends it there instead of failing opaquely.
	StateInstalling State = "installing"
	StateStarting   State = "starting"
	StateOnline     State = "online"
	StateError      State = "error"
)

type Config struct {
	Port            int    `json:"port"`
	Token           string `json:"-"`
	RPMPerIP        int    `json:"rpmPerIp"`
	ContextLimitKiB int    `json:"contextLimitKiB"`
	BrandResponse   string `json:"brandResponse"`
}

func (config Config) Validate() error {
	if config.Port < 1 || config.Port > 65535 || len(config.Token) < 32 || len(config.Token) > 512 || config.RPMPerIP < 0 || config.RPMPerIP > 1_000_000 || config.ContextLimitKiB < 0 || config.ContextLimitKiB > 2*1024*1024 || utf8.RuneCountInString(config.BrandResponse) > 500 || strings.ContainsAny(config.BrandResponse, "\r\n\x00") {
		return errors.New("invalid tunnel settings")
	}
	return nil
}

type Snapshot struct {
	State           State  `json:"state"`
	Port            int    `json:"port"`
	Address         string `json:"address"`
	RPMPerIP        int    `json:"rpmPerIp"`
	ContextLimitKiB int    `json:"contextLimitKiB"`
	BrandResponse   string `json:"brandResponse"`
	TokenConfigured bool   `json:"tokenConfigured"`
	Error           string `json:"error,omitempty"`
}
