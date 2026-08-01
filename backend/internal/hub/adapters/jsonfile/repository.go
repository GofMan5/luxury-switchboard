package jsonfile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/luxuryprivate/switchboard/backend/internal/hub/domain"
)

const maxStateBytes = 1 << 20

type Repository struct{ path string }

func New(path string) *Repository { return &Repository{path: path} }

func (repository *Repository) Transaction(ctx context.Context, operation func(*domain.State) (bool, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	lock, err := os.OpenFile(repository.path+".lock", os.O_CREATE|os.O_RDWR, 0o640)
	if err != nil {
		return errors.New("hub state unavailable")
	}
	defer lock.Close()
	if err := lockFile(ctx, lock); err != nil {
		return errors.New("hub state unavailable")
	}
	defer unlockFile(lock)
	if err := ctx.Err(); err != nil {
		return err
	}
	state, migrated, err := repository.load()
	if err != nil {
		return err
	}
	changed, err := operation(&state)
	if err != nil {
		return err
	}
	if err := state.Validate(); err != nil {
		return err
	}
	if changed || migrated {
		return repository.save(state)
	}
	return nil
}

func (repository *Repository) load() (domain.State, bool, error) {
	file, err := os.Open(repository.path)
	if err != nil {
		return domain.State{}, false, errors.New("hub state unavailable")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxStateBytes+1))
	if err != nil || len(raw) > maxStateBytes {
		return domain.State{}, false, errors.New("hub state unavailable")
	}
	var header struct {
		Version int `json:"v"`
	}
	if json.Unmarshal(raw, &header) != nil {
		return domain.State{}, false, errors.New("hub state unavailable")
	}
	switch header.Version {
	case 2:
		var wire struct {
			Version  int   `json:"v"`
			Revision int64 `json:"revision"`
			Tunnels  []struct {
				ID    string  `json:"id"`
				Owner *string `json:"owner"`
				State string  `json:"state"`
			} `json:"tunnels"`
		}
		if decodeExact(raw, &wire) != nil {
			return domain.State{}, false, errors.New("hub state unavailable")
		}
		state := domain.State{Revision: wire.Revision, Tunnels: make([]domain.Tunnel, 0, len(wire.Tunnels))}
		for _, tunnel := range wire.Tunnels {
			state.Tunnels = append(state.Tunnels, domain.Tunnel{ID: tunnel.ID, Owner: tunnel.Owner, State: tunnel.State})
		}
		return state, false, state.Validate()
	case 1:
		var wire struct {
			Version  int   `json:"v"`
			Revision int64 `json:"revision"`
			Tunnels  []struct {
				ID    string `json:"id"`
				Name  string `json:"name"`
				State string `json:"state"`
			} `json:"tunnels"`
		}
		if decodeExact(raw, &wire) != nil {
			return domain.State{}, false, errors.New("hub state unavailable")
		}
		state := domain.State{Revision: wire.Revision, Tunnels: make([]domain.Tunnel, 0, len(wire.Tunnels))}
		for _, tunnel := range wire.Tunnels {
			if tunnel.Name == "" {
				return domain.State{}, false, errors.New("hub state unavailable")
			}
			state.Tunnels = append(state.Tunnels, domain.Tunnel{ID: tunnel.ID, State: tunnel.State})
		}
		return state, true, state.Validate()
	default:
		return domain.State{}, false, errors.New("hub state unavailable")
	}
}

func (repository *Repository) save(state domain.State) error {
	type tunnel struct {
		ID    string  `json:"id"`
		Owner *string `json:"owner"`
		State string  `json:"state"`
	}
	value := struct {
		Version  int      `json:"v"`
		Revision int64    `json:"revision"`
		Tunnels  []tunnel `json:"tunnels"`
	}{Version: 2, Revision: state.Revision, Tunnels: make([]tunnel, 0, len(state.Tunnels))}
	for _, item := range state.Tunnels {
		value.Tunnels = append(value.Tunnels, tunnel{ID: item.ID, Owner: item.Owner, State: item.State})
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return errors.New("hub state unavailable")
	}
	raw = append(raw, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(repository.path), ".tunnels-*")
	if err != nil {
		return errors.New("hub state unavailable")
	}
	name := temporary.Name()
	defer os.Remove(name)
	if temporary.Chmod(0o640) != nil || writeSyncClose(temporary, raw) != nil || os.Rename(name, repository.path) != nil {
		return errors.New("hub state unavailable")
	}
	if directory, err := os.Open(filepath.Dir(repository.path)); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}

func decodeExact(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("extra JSON value")
	}
	return nil
}

func writeSyncClose(file *os.File, raw []byte) error {
	if _, err := file.Write(raw); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}
