package domain

import (
	"slices"
	"time"
)

// Journal entry kinds: what happened to a plugin.
const (
	JournalInstalled   = "installed"
	JournalUpdated     = "updated"
	JournalEnabled     = "enabled"
	JournalDisabled    = "disabled"
	JournalPermissions = "permissions"
	JournalLinked      = "linked"
	JournalUnlinked    = "unlinked"
	JournalRemoved     = "removed"
)

// Journal origins: where an installed or updated package came from. The
// link itself is never recorded.
const (
	OriginFile   = "file"
	OriginLink   = "link"
	OriginFolder = "folder"
)

// MaxJournalEntries is how many of the newest entries the journal keeps.
const MaxJournalEntries = 500

var journalKinds = []string{JournalInstalled, JournalUpdated, JournalEnabled, JournalDisabled, JournalPermissions, JournalLinked, JournalUnlinked, JournalRemoved}

// JournalEntry is one recorded change to an installed plugin. It names the
// plugin and permissions only: never storage, secrets or source links.
type JournalEntry struct {
	Seq         int64     `json:"seq"`
	At          time.Time `json:"at"`
	Kind        string    `json:"kind"`
	PluginID    string    `json:"pluginId"`
	Name        string    `json:"name"`
	Version     string    `json:"version,omitempty"`
	FromVersion string    `json:"fromVersion,omitempty"`
	Granted     []string  `json:"granted,omitempty"`
	Revoked     []string  `json:"revoked,omitempty"`
	Origin      string    `json:"origin,omitempty"`
}

// JournalName is the name recorded for a plugin: its package name, or the id
// when a broken record has none this version accepts.
func JournalName(id, name string) string {
	if validText(name, 1, MaxNameRunes, false) {
		return name
	}
	return id
}

// JournalVersion is the version recorded for a plugin, empty when a broken
// record has none this version accepts.
func JournalVersion(version string) string {
	if journalVersion(version) {
		return version
	}
	return ""
}

func journalVersion(version string) bool {
	return len(version) <= MaxVersionBytes && validText(version, 0, MaxVersionBytes, false)
}

// Valid reports whether a stored entry is one this version could have
// written.
func (entry JournalEntry) Valid() bool {
	return entry.Seq > 0 && !entry.At.IsZero() && slices.Contains(journalKinds, entry.Kind) &&
		ValidID(entry.PluginID) && validText(entry.Name, 1, MaxNameRunes, false) &&
		journalVersion(entry.Version) && journalVersion(entry.FromVersion) &&
		journalPermissions(entry.Granted) && journalPermissions(entry.Revoked) &&
		(entry.Origin == "" || entry.Origin == OriginFile || entry.Origin == OriginLink || entry.Origin == OriginFolder)
}

func journalPermissions(permissions []string) bool {
	if len(permissions) > MaxPermissions {
		return false
	}
	for _, permission := range permissions {
		if !validText(permission, 1, 64, false) {
			return false
		}
	}
	return true
}
