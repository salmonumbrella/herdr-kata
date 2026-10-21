package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
)

func openKataCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "usage: herdr-kata open-kata")
		return 2
	}
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := &herdr.Client{Path: herdr.SocketPath(cfg.Session)}
	// A global action has no TTY. The board hosts Kata and, when needed,
	// its project picker.
	err = h.Call(ctx, "plugin.pane.open", map[string]any{"plugin_id": "kata", "entrypoint": "board", "placement": "tab", "focus": true, "env": map[string]string{"HERDR_KATA_OPEN_KATA": "1"}}, nil)
	if err != nil {
		var apiError *herdr.Error
		if errors.As(err, &apiError) && apiError.Code == "ui_busy" {
			fmt.Fprintln(stdout, "Close the current modal, then try Open Kata")
			return 0
		}
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
