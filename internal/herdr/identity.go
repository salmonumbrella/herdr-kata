package herdr

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// SocketPath returns an empty path without an override, XDG_CONFIG_HOME or
// HOME. A missing config home never falls back to the current directory.
// Like Herdr 0.9.5's session::data_dir_for, default uses the config root;
// named sessions use its sessions/<name> subdirectory.
func SocketPath(session string) string {
	if p := os.Getenv("HERDR_SOCKET_PATH"); p != "" {
		return p
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	if session == "default" {
		return filepath.Join(dir, "herdr", "herdr.sock")
	}
	return filepath.Join(dir, "herdr", "sessions", session, "herdr.sock")
}

// SessionFromSocket recognizes named and default layouts for the session gate.
// It does not canonicalize the socket path or invert SocketPath.
func SessionFromSocket(path string) string {
	if filepath.Base(path) != "herdr.sock" {
		return ""
	}
	parent := filepath.Base(filepath.Dir(path))
	if parent == "herdr" && filepath.Base(filepath.Dir(filepath.Dir(path))) != "sessions" {
		return "default"
	}
	if parent == "." || parent == string(filepath.Separator) {
		return ""
	}
	return parent
}

// ReadServerID uses Lstat: a symlink, non-socket or unreadable identity is not
// proof of server generation. Device, inode and full ctime fence ID reuse.
func ReadServerID(path string) (ServerID, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return ServerID{}, err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return ServerID{}, fmt.Errorf("herdr: %s is not a socket", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ServerID{}, fmt.Errorf("herdr: socket identity unavailable")
	}
	return ServerID{Dev: uint64(stat.Dev), Ino: stat.Ino, CtimeNs: ctimeNs(stat)}, nil
}
