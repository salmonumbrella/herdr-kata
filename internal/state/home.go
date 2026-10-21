package state

import (
	"os"
	"os/user"
	"path/filepath"
)

// HomeDir uses HOME when absolute, then the OS account database. Fail loudly rather
// than make state depend on the current directory if neither identifies a home.
func HomeDir() string {
	if home, err := os.UserHomeDir(); err == nil && filepath.IsAbs(home) {
		return home
	}
	if account, err := user.Current(); err == nil && filepath.IsAbs(account.HomeDir) {
		return account.HomeDir
	}
	panic("cannot determine an absolute home directory")
}
