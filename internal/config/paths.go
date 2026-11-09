package config

import (
	"os"
	"path/filepath"
)

// Path is the config file: $XDG_CONFIG_HOME/watts/config.toml, default
// ~/.config/watts/config.toml.
func Path() string {
	return filepath.Join(xdg("XDG_CONFIG_HOME", ".config"), "watts", "config.toml")
}

// DataDir holds the store and tariff history: $XDG_DATA_HOME/watts, default
// ~/.local/share/watts.
func DataDir() string {
	return filepath.Join(xdg("XDG_DATA_HOME", filepath.Join(".local", "share")), "watts")
}

// StorePath is the SQLite store in DataDir.
func StorePath() string { return filepath.Join(DataDir(), "watts.db") }

// TariffPath is the tariff history file in DataDir.
func TariffPath() string { return filepath.Join(DataDir(), "tariffs.toml") }

func xdg(env, fallback string) string {
	if v := os.Getenv(env); v != "" && filepath.IsAbs(v) {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, fallback)
}
