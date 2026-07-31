//go:build !windows

package pythonconfig

func LoadDefault() (State, bool, error) { return State{}, false, nil }
