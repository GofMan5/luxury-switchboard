package domain

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxNetworkHosts bounds network.hosts.
	MaxNetworkHosts = 16
	// MaxHostBytes bounds one network.hosts entry, wildcard included.
	MaxHostBytes = 253
)

var hostLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// privateSuffixes only ever name this machine or the local network.
var privateSuffixes = []string{"localhost", "local", "internal"}

// Network is the manifest's allowlist for net:fetch. Each host is a
// lowercase DNS name; "*.example.com" matches every subdomain of
// example.com but not example.com itself.
type Network struct {
	Hosts []string `json:"hosts"`
}

func (network Network) validate() error {
	if len(network.Hosts) < 1 || len(network.Hosts) > MaxNetworkHosts {
		return ValidationError{"network.hosts", fmt.Sprintf("must list 1-%d hosts", MaxNetworkHosts)}
	}
	for index, host := range network.Hosts {
		field := fmt.Sprintf("network.hosts[%d]", index)
		if problem := HostProblem(host); problem != "" {
			return ValidationError{field, problem}
		}
		if slices.Contains(network.Hosts[:index], host) {
			return ValidationError{field, "repeats another host"}
		}
	}
	return nil
}

// HostProblem says why host cannot be a network.hosts entry, or "". The
// rules match the shell's manifest check; the address a name resolves to is
// checked again when a request connects.
func HostProblem(host string) string {
	if host == "" {
		return "must be a host name"
	}
	if len(host) > MaxHostBytes {
		return fmt.Sprintf("is longer than %d characters", MaxHostBytes)
	}
	name := strings.TrimPrefix(host, "*.")
	if strings.ContainsAny(name, ":/?#@") || strings.ContainsFunc(name, unicode.IsSpace) {
		return "must be a bare host name, without a scheme, port or path"
	}
	labels := strings.Split(name, ".")
	for _, label := range labels {
		if !hostLabel.MatchString(label) {
			return "must be a lowercase DNS name such as api.example.com or *.example.com"
		}
	}
	last := labels[len(labels)-1]
	if slices.Contains(privateSuffixes, last) {
		return "names this machine or the local network, which plugins cannot reach"
	}
	// No top-level domain is all digits, so this also refuses IPv4 literals.
	if strings.Trim(last, "0123456789") == "" {
		return "must be a name, not an IP address"
	}
	if len(labels) < 2 {
		return "must have a domain, such as example.com"
	}
	return ""
}

// Allows reports whether the network allowlist admits hostname, which must
// already be lower case and free of a trailing dot.
func (network Network) Allows(hostname string) bool {
	for _, host := range network.Hosts {
		if suffix, wildcard := strings.CutPrefix(host, "*."); wildcard {
			if strings.HasSuffix(hostname, "."+suffix) && len(hostname) > len(suffix)+1 {
				return true
			}
		} else if hostname == host {
			return true
		}
	}
	return false
}

// Widens reports whether after admits a host network did not, so a grant of
// net:fetch made for network does not cover after. A nil network admits none.
func (network *Network) Widens(after *Network) bool {
	if after == nil {
		return false
	}
	for _, host := range after.Hosts {
		if network == nil || !slices.Contains(network.Hosts, host) {
			return true
		}
	}
	return false
}

// DevLink records the source folder a plugin is loaded from while its author
// works on it. Problem is the last reason the folder could not be loaded;
// the last good version stays installed meanwhile.
type DevLink struct {
	Path     string    `json:"path"`
	LoadedAt time.Time `json:"loadedAt"`
	Problem  string    `json:"problem,omitempty"`
}

const (
	// MaxDevLinks bounds the plugins loaded from source folders at once.
	MaxDevLinks = 4
	// MaxDevPathRunes bounds a linked folder's path.
	MaxDevPathRunes = 1024
	// MaxDevProblemRunes bounds the reason a linked folder did not load.
	MaxDevProblemRunes = 2000
)

// ValidDevPath reports whether path can name a linked source folder: an
// absolute, clean path of bounded length without control characters.
func ValidDevPath(path string) bool {
	return validText(path, 1, MaxDevPathRunes, false) && filepath.IsAbs(path) && filepath.Clean(path) == path
}

// DevProblem makes a load problem storable: valid UTF-8, control characters
// other than newlines as spaces, at most MaxDevProblemRunes with a marked cut.
func DevProblem(problem string) string {
	problem = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) && character != '\n' {
			return ' '
		}
		return character
	}, strings.ToValidUTF8(problem, "\uFFFD"))
	if utf8.RuneCountInString(problem) <= MaxDevProblemRunes {
		return problem
	}
	runes := []rune(problem)
	return string(runes[:MaxDevProblemRunes-1]) + "…"
}

func (link DevLink) validate() error {
	switch {
	case !ValidDevPath(link.Path):
		return ValidationError{"dev.path", "must be an absolute folder path"}
	case link.LoadedAt.IsZero():
		return ValidationError{"dev.loadedAt", "is missing"}
	case !validText(link.Problem, 0, MaxDevProblemRunes, true):
		return ValidationError{"dev.problem", fmt.Sprintf("must be at most %d characters", MaxDevProblemRunes)}
	}
	return nil
}
