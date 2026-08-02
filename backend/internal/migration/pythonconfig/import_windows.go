//go:build windows

package pythonconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/secretstore"
	keydomain "github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
	providerdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
	routedomain "github.com/luxuryprivate/switchboard/backend/internal/slices/routes/domain"
	tunneldomain "github.com/luxuryprivate/switchboard/backend/internal/slices/tunnel/domain"
)

var legacyMagic = []byte{'P', 'S', 'C', 'F', 1}

type document struct {
	Schema          int               `json:"schema"`
	Providers       []provider        `json:"providers"`
	Active          string            `json:"active"`
	ModelRoutes     map[string]string `json:"model_routes"`
	ModelRouteOrder []string          `json:"model_route_order"`
	Tunnel          *tunnel           `json:"tunnel"`
}
type provider struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Upstream          string `json:"upstream"`
	AuthMode          string `json:"auth_mode"`
	Cache1H           bool   `json:"cache_1h"`
	RPM               int    `json:"rpm"`
	EnvironmentKeyRPM *int   `json:"environment_key_rpm,omitempty"`
	Keys              []key  `json:"keys"`
}
type key struct {
	Secret string `json:"key"`
	RPM    int    `json:"rpm"`
	Proxy  string `json:"proxy"`
}
type tunnel struct {
	AccessToken      string            `json:"access_token"`
	AllowedModels    []string          `json:"allowed_models"`
	ContextLimitKiB  int               `json:"context_limit_kib"`
	ModelRoutes      map[string]string `json:"model_routes"`
	ProviderID       string            `json:"provider_id"`
	PublisherProfile string            `json:"publisher_profile"`
	RPMPerIP         int               `json:"rpm_per_ip"`
}

func LoadDefault() (State, bool, error) {
	root := os.Getenv("LOCALAPPDATA")
	if root == "" {
		return State{}, false, nil
	}
	file, err := os.Open(filepath.Join(root, "ProviderSwitchboard", "config.v1.dpapi"))
	if errors.Is(err, os.ErrNotExist) {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, errors.New("legacy config is unreadable")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 4*1024*1024+1))
	if err != nil || len(raw) <= len(legacyMagic) || len(raw) > 4*1024*1024 || !bytes.Equal(raw[:len(legacyMagic)], legacyMagic) {
		return State{}, false, errors.New("legacy config is unreadable")
	}
	plain, err := secretstore.UnprotectLegacy(raw[len(legacyMagic):])
	if err != nil {
		return State{}, false, err
	}
	defer clear(plain)
	var saved document
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&saved) != nil || decoder.Decode(&struct{}{}) != io.EOF || saved.Schema != 1 || len(saved.Providers) == 0 || len(saved.Providers) > 64 {
		return State{}, false, errors.New("legacy config is invalid")
	}
	state := State{ActiveID: saved.Active}
	for _, item := range saved.Providers {
		provider, err := providerdomain.New(providerdomain.Params{
			ID: item.ID, Name: item.Name, BaseURL: item.Upstream,
			AuthMode: providerdomain.AuthMode(item.AuthMode), Dialect: providerdomain.DialectAuto,
			ModelsPath: "/v1/models", ImageCompat: item.ID == "echo", RPM: item.RPM, CacheTTL: cacheDuration(item.Cache1H),
			Enabled: true, Builtin: item.ID == "local" || item.ID == "echo",
		})
		if err != nil {
			return State{}, false, errors.New("legacy provider is invalid")
		}
		state.Providers = append(state.Providers, provider)
		for index, oldKey := range item.Keys {
			value, err := keydomain.NewKey(keydomain.Params{
				ProviderID: item.ID, Label: "Migrated key " + strconv.Itoa(index+1),
				Secret: oldKey.Secret, RPM: oldKey.RPM, ProxyURL: oldKey.Proxy, Priority: index,
			})
			if err != nil {
				return State{}, false, errors.New("legacy key is invalid")
			}
			state.Keys = append(state.Keys, value)
		}
	}
	for model, providerID := range saved.ModelRoutes {
		state.Routes = append(state.Routes, routedomain.Assignment{Target: routedomain.TargetRelay, PublicModel: model, UpstreamModel: model, ProviderID: providerID, Enabled: true})
	}
	if saved.Tunnel != nil {
		for _, model := range saved.Tunnel.AllowedModels {
			providerID := saved.Tunnel.ModelRoutes[model]
			if providerID == "" {
				providerID = saved.Tunnel.ProviderID
			}
			state.Routes = append(state.Routes, routedomain.Assignment{Target: routedomain.TargetTunnel, PublicModel: model, UpstreamModel: model, ProviderID: providerID, Enabled: true})
		}
		state.Tunnel = tunneldomain.Config{
			Port: 8797, Token: saved.Tunnel.AccessToken, RPMPerIP: saved.Tunnel.RPMPerIP,
			ContextLimitKiB: saved.Tunnel.ContextLimitKiB, PublisherProfile: saved.Tunnel.PublisherProfile,
			BrandResponse: "Luxury Private лучший приватный софт для абузов - @Luxuryprivate_bot",
		}
		state.HasTunnel = state.Tunnel.Validate() == nil
	}
	if err := validateState(state); err != nil {
		return State{}, false, err
	}
	return state, true, nil
}

func cacheDuration(enabled bool) time.Duration {
	if enabled {
		return time.Hour
	}
	return 0
}
func validateState(state State) error {
	providers := make(map[string]struct{}, len(state.Providers))
	for _, provider := range state.Providers {
		if provider.ID == "" {
			return errors.New("legacy provider is invalid")
		}
		if _, duplicate := providers[provider.ID]; duplicate {
			return errors.New("legacy provider is duplicated")
		}
		providers[provider.ID] = struct{}{}
	}
	if _, exists := providers[state.ActiveID]; !exists {
		return errors.New("legacy active provider is invalid")
	}
	keys := make(map[string]struct{}, len(state.Keys))
	for _, key := range state.Keys {
		if key.ID == "" {
			return errors.New("legacy key is invalid")
		}
		if _, exists := providers[key.ProviderID]; !exists {
			return errors.New("legacy key provider is invalid")
		}
		if _, duplicate := keys[key.ID]; duplicate {
			return errors.New("legacy key is duplicated")
		}
		keys[key.ID] = struct{}{}
	}
	routes := make(map[string]struct{}, len(state.Routes))
	for _, route := range state.Routes {
		if route.Validate() != nil {
			return errors.New("legacy route is invalid")
		}
		if _, exists := providers[route.ProviderID]; !exists {
			return errors.New("legacy route provider is invalid")
		}
		key := string(route.Target) + "\x00" + route.PublicModel
		if _, duplicate := routes[key]; duplicate {
			return errors.New("legacy route is duplicated")
		}
		routes[key] = struct{}{}
	}
	return nil
}
