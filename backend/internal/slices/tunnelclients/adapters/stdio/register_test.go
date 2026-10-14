package clientstdio_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	clientsqlite "github.com/luxuryprivate/switchboard/backend/internal/slices/tunnelclients/adapters/sqlite"
	clientstdio "github.com/luxuryprivate/switchboard/backend/internal/slices/tunnelclients/adapters/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/tunnelclients/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/tunnelclients/domain"
)

type reply struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	OK      bool   `json:"ok"`
	Payload struct {
		Clients   []domain.Client `json:"clients"`
		Available int             `json:"available"`
	} `json:"payload"`
	Error *platform.RPCError `json:"error"`
}

func exchange(t *testing.T, service *application.Service, commands ...platform.Request) map[string]reply {
	t.Helper()
	var input, output strings.Builder
	for _, command := range commands {
		command.Version, command.Type = platform.ProtocolVersion, "command"
		raw, err := json.Marshal(command)
		if err != nil {
			t.Fatal(err)
		}
		input.Write(raw)
		input.WriteByte('\n')
	}
	server := platform.NewServer(strings.NewReader(input.String()), &output, 1)
	clientstdio.Register(server, service)
	if err := server.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	results := make(map[string]reply)
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		if len(line)+1 > platform.MaxFrameBytes {
			t.Fatalf("reply exceeds frame budget: %d bytes", len(line)+1)
		}
		var frame reply
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			t.Fatal(err)
		}
		if frame.Type == "result" {
			results[frame.ID] = frame
		}
	}
	return results
}

func TestASavedBanAnswersSuccessfullyEvenWhenTheClientRegistryExceedsOneFrame(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "clients.db")
	store, err := clientsqlite.Open(path, 72)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(ctx)
	service := application.NewService(store)
	const count = 2_000
	for index := range count {
		service.Queue(fmt.Sprintf("2001:db8::%x", index), 1)
	}
	const bannedIP = "2001:db8::7cf"
	payload, err := json.Marshal(domain.Profile{IP: bannedIP, Banned: true, Note: "synthetic abuse"})
	if err != nil {
		t.Fatal(err)
	}
	results := exchange(t, service,
		platform.Request{ID: "ban", Method: "clients.profile", Payload: payload},
		platform.Request{ID: "list", Method: "clients.list"},
	)
	for _, id := range []string{"ban", "list"} {
		frame, exists := results[id]
		if !exists || !frame.OK || frame.Error != nil {
			t.Fatalf("%s reported failure after applying the ban: %v", id, frame.Error)
		}
		if frame.Payload.Available != count || len(frame.Payload.Clients) == 0 || len(frame.Payload.Clients) >= count {
			t.Fatalf("%s did not return a truthful bounded list: %d of %d", id, len(frame.Payload.Clients), frame.Payload.Available)
		}
		found := false
		for _, client := range frame.Payload.Clients {
			if client.IP == bannedIP {
				found = client.Banned && client.Note == "synthetic abuse"
			}
		}
		if !found {
			t.Fatalf("%s did not show the applied ban on the recent client", id)
		}
	}
	if !service.Banned(bannedIP) {
		t.Fatal("successful reply did not enforce the ban")
	}
	if err := store.Close(ctx); err != nil {
		t.Fatal(err)
	}
	reopened, err := clientsqlite.Open(path, 72)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close(ctx)
	restarted := application.NewService(reopened)
	if err := restarted.LoadProfiles(ctx); err != nil {
		t.Fatal(err)
	}
	if !restarted.Banned(bannedIP) {
		t.Fatal("successful mutation reply did not describe a durable ban")
	}
}
