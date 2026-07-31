package domain

import "time"

type Client struct {
	IP        string    `json:"ip"`
	ActualRPM int       `json:"actualRpm"`
	Count     int64     `json:"count"`
	Active    int       `json:"active"`
	Queued    int       `json:"queued"`
	LastSeen  time.Time `json:"lastSeen"`
	State     string    `json:"state"`
}
type Event struct {
	ID        string    `json:"id"`
	IP        string    `json:"ip"`
	Time      time.Time `json:"time"`
	State     string    `json:"state"`
	Method    string    `json:"method"`
	Path      string    `json:"path"`
	Model     string    `json:"model"`
	Status    int       `json:"status"`
	LatencyMS float64   `json:"latencyMs"`
	BytesIn   int64     `json:"bytesIn"`
	BytesOut  int64     `json:"bytesOut"`
	ErrorCode string    `json:"errorCode,omitempty"`
}
type Start struct {
	IP      string
	Method  string
	Path    string
	Model   string
	BytesIn int64
}
type Finish struct {
	Status    int
	BytesOut  int64
	ErrorCode string
	Duration  time.Duration
}
