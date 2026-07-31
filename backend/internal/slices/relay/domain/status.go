package domain

type State string

const (
	StateStarting State = "starting"
	StateLive     State = "live"
	StateStopped  State = "stopped"
	StateError    State = "error"
)

type Snapshot struct {
	State   State  `json:"state"`
	Address string `json:"address"`
	Port    int    `json:"port"`
	Error   string `json:"error,omitempty"`
}
