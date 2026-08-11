package appdata

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

func Root() (string, error) {
	root := ""
	directory := "provider-switchboard"
	if runtime.GOOS == "windows" {
		root = os.Getenv("LOCALAPPDATA")
		directory = "ProviderSwitchboard"
	} else {
		var err error
		root, err = os.UserConfigDir()
		if err != nil {
			return "", errors.New("user data directory is unavailable")
		}
	}
	if root == "" || !filepath.IsAbs(root) {
		return "", errors.New("user data directory is unavailable")
	}
	return filepath.Join(root, directory), nil
}
