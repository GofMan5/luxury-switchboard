package application

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/backup/domain"
)

// Sources reads the live configuration for export. Implementations decrypt
// what their slice persists; the backup itself stays plain JSON.
type Sources interface {
	Providers() ([]domain.ProviderEntry, error)
	Keys() ([]domain.KeyEntry, error)
	Routes() ([]domain.RouteEntry, error)
}

// Sinks restore entries into their slices' own managers, so validation and
// persistence stay where they always were. A sink reports whether the entry
// was added; false means it already existed.
type Sinks interface {
	AddProvider(entry domain.ProviderEntry) (bool, error)
	AddKey(entry domain.KeyEntry) (bool, error)
	UpsertRoute(entry domain.RouteEntry) (bool, error)
	ProviderExists(id string) bool
}

type Service struct {
	sources Sources
	sinks   Sinks
	now     func() time.Time
}

func NewService(sources Sources, sinks Sinks) (*Service, error) {
	if sources == nil || sinks == nil {
		return nil, errors.New("backup dependencies are invalid")
	}
	return &Service{sources: sources, sinks: sinks, now: time.Now}, nil
}

// Export writes the whole configuration as plain JSON into the folder and
// returns the file's full path. The name carries the moment so consecutive
// exports never overwrite each other.
func (service *Service) Export(folder string) (string, error) {
	providers, err := service.sources.Providers()
	if err != nil {
		return "", fmt.Errorf("providers could not be read: %w", err)
	}
	keys, err := service.sources.Keys()
	if err != nil {
		return "", fmt.Errorf("keys could not be read: %w", err)
	}
	routes, err := service.sources.Routes()
	if err != nil {
		return "", fmt.Errorf("routes could not be read: %w", err)
	}
	sort.SliceStable(providers, func(i, j int) bool { return providers[i].ID < providers[j].ID })
	sort.SliceStable(keys, func(i, j int) bool {
		if keys[i].ProviderID != keys[j].ProviderID {
			return keys[i].ProviderID < keys[j].ProviderID
		}
		return keys[i].Label < keys[j].Label
	})
	document := domain.Document{
		Version:    domain.FormatVersion,
		ExportedAt: service.now(),
		Note:       "Plain-text Switchboard backup: this file carries provider keys unencrypted. Store it wherever you trust.",
		Providers:  providers,
		Keys:       keys,
		Routes:     routes,
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return "", fmt.Errorf("backup folder is unavailable: %w", err)
	}
	path := filepath.Join(folder, "Luxury-Switchboard-backup-"+service.now().Format("20060102-150405")+".json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return "", fmt.Errorf("backup file could not be written: %w", err)
	}
	return path, nil
}

// Import restores a backup document: entries that already exist are skipped,
// entries that fail their slice's validation are counted, and the rest land.
// Nothing is deleted — a restore adds, so a live setup survives meeting its
// own backup.
func (service *Service) Import(content string) (domain.ImportReport, error) {
	var document domain.Document
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &document); err != nil {
		return domain.ImportReport{}, errors.New("the file is not a Switchboard backup")
	}
	if document.Version != domain.FormatVersion {
		return domain.ImportReport{}, errors.New("the backup was written by another version")
	}
	report := domain.ImportReport{}
	for _, entry := range document.Providers {
		added, err := service.sinks.AddProvider(entry)
		if err != nil {
			report.Failed++
			continue
		}
		if added {
			report.ProvidersAdded++
		} else {
			report.ProvidersSkipped++
		}
	}
	for _, entry := range document.Keys {
		if !service.sinks.ProviderExists(entry.ProviderID) {
			// The key's provider is not part of this backup and not part of
			// the live setup: importing the key would configure a credential
			// for a provider that does not exist.
			report.Failed++
			continue
		}
		added, err := service.sinks.AddKey(entry)
		if err != nil {
			report.Failed++
			continue
		}
		if added {
			report.KeysAdded++
		} else {
			report.KeysSkipped++
		}
	}
	for _, entry := range document.Routes {
		if !service.sinks.ProviderExists(entry.ProviderID) {
			report.RoutesSkipped++
			continue
		}
		added, err := service.sinks.UpsertRoute(entry)
		if err != nil {
			report.Failed++
			continue
		}
		if added {
			report.RoutesAdded++
		} else {
			report.RoutesSkipped++
		}
	}
	return report, nil
}
