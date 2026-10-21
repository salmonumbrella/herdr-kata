package main

import (
	"bytes"
	"encoding/json"
	"github.com/salmonumbrella/herdr-kata/internal/herdr/herdrtest"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenKataActionAsksProject(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", root)
	t.Setenv("HERDR_PLUGIN_STATE_DIR", root)
	if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte(`session="work"
executor="worker"
[kata]
bin="/usr/local/bin/kata"
[profiles.default]
kind="codex"
[[projects]]
name="example-project"
actor="worker"
[[projects]]
name="team-project"
actor="worker"
`), 0600); err != nil {
		t.Fatal(err)
	}
	path := herdrtest.SocketPath(t)
	t.Setenv("HERDR_SOCKET_PATH", path)
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	received := make(chan map[string]any, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var req map[string]any
		if json.NewDecoder(conn).Decode(&req) == nil {
			received <- req
			_ = json.NewEncoder(conn).Encode(map[string]any{"id": "1", "result": map[string]string{"type": "ok"}})
		}
	}()
	var out, stderr bytes.Buffer
	if code := run([]string{"open-kata"}, &out, &stderr); code != 0 {
		t.Fatal(code, stderr.String())
	}
	req := <-received
	params := req["params"].(map[string]any)
	if req["method"] != "plugin.pane.open" || params["plugin_id"] != "kata" || params["entrypoint"] != "board" || params["env"].(map[string]any)["HERDR_KATA_OPEN_KATA"] != "1" {
		t.Fatal("action did not open project picker", req)
	}
}
