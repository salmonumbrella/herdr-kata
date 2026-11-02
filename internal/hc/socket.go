package hc

import (
	"fmt"
	"github.com/BurntSushi/toml"
	"os"
	"path/filepath"
)

// Socket reports the endpoint selected with HC_SOCKET intentionally removed.
func Socket(session string) (string, error) {
	home := os.Getenv("HOME")
	if home == "" {
		home = "/"
	}
	config := os.Getenv("XDG_CONFIG_HOME")
	if config == "" {
		config = filepath.Join(home, ".config")
	}
	var cfg struct{ Socket string }
	_, err := toml.DecodeFile(filepath.Join(config, "herdr-core", "config.toml"), &cfg)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if cfg.Socket != "" {
		return filepath.Abs(cfg.Socket)
	}
	run := os.Getenv("XDG_RUNTIME_DIR")
	if run == "" {
		candidate := fmt.Sprintf("/run/user/%d", os.Getuid())
		if info, e := os.Stat(candidate); e == nil && info.IsDir() {
			run = candidate
		}
	}
	if run != "" {
		return filepath.Join(run, "herdr-core", session+".sock"), nil
	}
	root := os.Getenv("XDG_STATE_HOME")
	if root == "" {
		root = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(root, "herdr-core", session, session+".sock"), nil
}
