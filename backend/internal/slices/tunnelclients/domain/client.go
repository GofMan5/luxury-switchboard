package domain

import (
	"errors"
	"net"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const maxNoteRunes = 500

type Client struct {
	IP        string    `json:"ip"`
	ActualRPM int       `json:"actualRpm"`
	Count     int64     `json:"count"`
	Active    int       `json:"active"`
	Queued    int       `json:"queued"`
	Refused   int64     `json:"refused"`
	LastSeen  time.Time `json:"lastSeen"`
	State     string    `json:"state"`
	Banned    bool      `json:"banned"`
	Note      string    `json:"note"`
}

// Profile is the owner-only governance record for one client address. It never
// leaves the local control plane.
type Profile struct {
	IP     string `json:"ip"`
	Banned bool   `json:"banned"`
	Note   string `json:"note"`
}

// Canonical returns the profile with the address written exactly as the gateway
// reports it, so a stored decision and a live lookup can never disagree.
func (profile Profile) Canonical() (Profile, error) {
	address := net.ParseIP(strings.TrimSpace(profile.IP))
	if address == nil {
		return Profile{}, errors.New("client address is invalid")
	}
	profile.IP = address.String()
	profile.Note = strings.TrimSpace(profile.Note)
	if utf8.RuneCountInString(profile.Note) > maxNoteRunes || !printableNote(profile.Note) {
		return Profile{}, errors.New("client note is invalid")
	}
	return profile, nil
}

// Notes are a single-line owner label, so control characters and bidirectional
// formatting that could reorder the rendered text are refused.
func printableNote(note string) bool {
	for _, character := range note {
		if unicode.IsControl(character) {
			return false
		}
		// Bidi marks, overrides and isolates would let a note reorder its own text.
		if character >= '‎' && character <= '‏' ||
			character >= '‪' && character <= '‮' ||
			character >= '⁦' && character <= '⁩' {
			return false
		}
	}
	return true
}

// Empty reports a profile that carries no owner decision and can be forgotten.
func (profile Profile) Empty() bool {
	return !profile.Banned && strings.TrimSpace(profile.Note) == ""
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
