package daemon

import (
	"errors"
	"io/fs"
	"path/filepath"

	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func saveStartupErrors(l state.Layout, problems map[string]string) error {
	return state.WriteJSON(state.OSFS{}, filepath.Join(l.Root, "project-startup.json"), problems)
}
func readStartupErrors(l state.Layout) (map[string]string, error) {
	problems := map[string]string{}
	err := state.ReadJSON(state.OSFS{}, filepath.Join(l.Root, "project-startup.json"), 64<<10, &problems)
	if errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	return problems, err
}
