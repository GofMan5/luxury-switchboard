package routestdio

import (
	"fmt"
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/secretstore"
)

func TestSecureStorageFailureHasActionableError(t *testing.T) {
	err := operationError(fmt.Errorf("routes: %w", secretstore.ErrUnavailable), "route_update_failed", "Route could not be saved")
	if err.Code != "secure_storage_unavailable" || err.Message == "Route could not be saved" {
		t.Fatalf("secure storage failure stayed generic: %+v", err)
	}
}
