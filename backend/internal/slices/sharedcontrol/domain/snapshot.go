package domain

type Tunnel struct {
	Position int    `json:"position"`
	Name     string `json:"name"`
	State    string `json:"state"`
}

type Snapshot struct {
	Available bool     `json:"available"`
	Revision  int64    `json:"revision"`
	Tunnels   []Tunnel `json:"tunnels"`
	Error     string   `json:"error"`
	Stale     bool     `json:"stale,omitempty"`
}
