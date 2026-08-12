//go:build public

package bootstrap

import (
	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
)

// The public edition has no publishing stack at all: no gateway, no client
// governance, no shared control and no SSH publisher anywhere in the binary.
func registerEdition(*platform.Server, editionDependencies) (editionRuntime, error) {
	return editionRuntime{}, nil
}
