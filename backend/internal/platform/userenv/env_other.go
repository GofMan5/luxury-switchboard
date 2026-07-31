//go:build !windows

package userenv

import (
	"os"
	"strings"
)

func Get(name string) string { return strings.TrimSpace(os.Getenv(name)) }
