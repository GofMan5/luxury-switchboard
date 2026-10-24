package dpapi

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/plugins/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/plugins/domain"
)

// The capacity check runs before anything is encrypted, so it needs no
// secure storage on any platform.
func TestAnOversizedDocumentIsReportedAsFullBeforeWriting(t *testing.T) {
	repository := New(filepath.Join(t.TempDir(), "plugins.dpapi"))
	huge := domain.Plugin{Package: domain.Package{Schema: 1, ID: "acme.huge", Script: strings.Repeat("a", maxPlaintext)}}
	if err := repository.Save(context.Background(), []domain.Plugin{huge}); !errors.Is(err, application.ErrCapacity) {
		t.Fatalf("an oversized document: %v", err)
	}
}

func TestAnUnresolvedPathIsNotAnEmptyStore(t *testing.T) {
	repository := New("")
	if _, err := repository.Load(context.Background()); err == nil {
		t.Fatal("an unresolved path loaded as empty")
	}
	if repository.Save(context.Background(), nil) == nil {
		t.Fatal("an unresolved path accepted a save")
	}
}
