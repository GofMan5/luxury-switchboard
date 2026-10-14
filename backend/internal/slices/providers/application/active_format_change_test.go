package application

import (
	"github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
	"testing"
)

func TestActiveEndpointFormatChangesCancelOldRelay(t *testing.T) {
	for _, field := range []string{"format", "chat-path", "metadata"} {
		t.Run(field, func(t *testing.T) {
			current := provider(t, "fixture", true)
			catalog, _ := NewCatalog([]domain.Provider{current}, "fixture")
			changed := 0
			catalog.OnActivated(func(string) { changed++ })
			next := current
			switch field {
			case "format":
				next.Format = "chat"
			case "chat-path":
				next.ChatPath = "/chat-completion"
			case "metadata":
				next.Name = "New label"
				next.RPM = 42
			}
			if err := catalog.Replace([]domain.Provider{next}, "fixture"); err != nil {
				t.Fatal(err)
			}
			want := 1
			if field == "metadata" {
				want = 0
			}
			if changed != want {
				t.Fatalf("active %s edit emits cancellation=%d want=%d", field, changed, want)
			}
		})
	}
}
