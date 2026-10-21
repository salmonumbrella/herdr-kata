package herdr_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/herdr/herdrtest"
)

// Serve independent wire fixtures rather than encoding the client's types.
func wireServer(t testing.TB, respond func(map[string]json.RawMessage) string) *herdr.Client {
	t.Helper()
	path := herdrtest.SocketPath(t)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	t.Cleanup(func() { ln.Close(); <-done })
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			func() {
				defer c.Close()
				var req map[string]json.RawMessage
				if err := json.NewDecoder(c).Decode(&req); err != nil {
					return
				}
				fmt.Fprint(c, respond(req))
			}()
		}
	}()
	return &herdr.Client{Path: path}
}
func reply(req map[string]json.RawMessage, body string) string {
	return fmt.Sprintf("{\"id\":%s,%s}\n", req["id"], body)
}

func TestCallRoundTrip(t *testing.T) {
	c := wireServer(t, func(req map[string]json.RawMessage) string {
		if string(req["method"]) != `"ping"` || string(req["params"]) != `{}` {
			t.Errorf("request: %s", req)
		}
		return reply(req, `"result":{"type":"pong","version":"0.9.1","protocol":22,"future":true}`)
	})
	p, err := c.Ping(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p.Version != "0.9.1" || p.Protocol != 22 {
		t.Fatalf("pong: %+v", p)
	}
	// A second request must use a fresh connection.
	if _, err := c.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestCallErrorEnvelope(t *testing.T) {
	c := wireServer(t, func(req map[string]json.RawMessage) string {
		return reply(req, `"error":{"code":"agent_blocked","message":"approval required"}`)
	})
	_, err := c.AgentGet(context.Background(), "worker")
	var apiErr *herdr.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "agent_blocked" || apiErr.Message != "approval required" {
		t.Fatalf("error: %v", err)
	}
}
func TestSessionFromSocket(t *testing.T) {
	for _, tt := range []struct{ path, want string }{
		{"/home/example/.config/herdr/herdr.sock", "default"},
		{"/home/example/.config/herdr/sessions/build/herdr.sock", "build"},
		{"/tmp/sessions/team/herdr.sock", "team"},
		{"/tmp/not-a-socket", ""},
	} {
		if got := herdr.SessionFromSocket(tt.path); got != tt.want {
			t.Errorf("%s: %q want %q", tt.path, got, tt.want)
		}
	}
}
func TestSocketPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HERDR_SOCKET_PATH", "")
	want := filepath.Join(os.Getenv("HOME"), ".config/herdr/sessions/build/herdr.sock")
	if got := herdr.SocketPath("build"); got != want {
		t.Fatalf("path %q", got)
	}
	t.Setenv("HERDR_SOCKET_PATH", "/tmp/explicit.sock")
	if got := herdr.SocketPath("build"); got != "/tmp/explicit.sock" {
		t.Fatal(got)
	}
}
func TestAgentDecodeOmittedNullZero(t *testing.T) {
	for _, tt := range []struct {
		fields string
		seq    *uint64
	}{
		{``, nil}, {`,"completion_seq":null,"agent_session":null`, nil}, {`,"completion_seq":0`, new(uint64)},
	} {
		t.Run(tt.fields, func(t *testing.T) {
			c := wireServer(t, func(req map[string]json.RawMessage) string {
				return reply(req, `"result":{"type":"agent_info","agent":{"name":"worker","pane_id":"w1:p1","terminal_id":"term1","workspace_id":"w1","tab_id":"w1:t1","focused":false,"revision":1,"agent_status":"idle","interactive_ready":true`+tt.fields+`}}`)
			})
			a, err := c.AgentGet(context.Background(), "worker")
			if err != nil {
				t.Fatal(err)
			}
			if a.Status != "idle" || !a.InteractiveReady || a.Session != nil || !reflect.DeepEqual(a.CompletionSeq, tt.seq) {
				t.Fatalf("agent %+v", a)
			}
		})
	}
}
func TestAgentPromptWaitParams(t *testing.T) {
	c := wireServer(t, func(req map[string]json.RawMessage) string {
		var method string
		json.Unmarshal(req["method"], &method)
		want := `{"target":"worker","until":["idle","done","blocked"],"timeout_ms":1234}`
		if method == "agent.prompt" {
			want = `{"target":"worker","text":"Read /tmp/prompt.md","wait":{"until":["idle","done","blocked"],"timeout_ms":1234}}`
		}
		var gotObj, wantObj any
		json.Unmarshal(req["params"], &gotObj)
		json.Unmarshal([]byte(want), &wantObj)
		if !reflect.DeepEqual(gotObj, wantObj) {
			t.Errorf("%s params %s want %s", method, req["params"], want)
		}
		typ := "agent_info"
		if method == "agent.prompt" {
			typ = "agent_prompted"
		}
		return reply(req, `"result":{"type":"`+typ+`","agent":{"name":"worker","agent_status":"done","completion_seq":7,"agent_session":{"source":"hook","agent":"codex","kind":"id","value":"session-1"}}}`)
	})
	until := []string{"idle", "done", "blocked"}
	a, err := c.AgentPrompt(context.Background(), "worker", "Read /tmp/prompt.md", until, 1234*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if a.Session == nil || a.Session.Value != "session-1" || a.CompletionSeq == nil || *a.CompletionSeq != 7 {
		t.Fatalf("agent %+v", a)
	}
	if _, err := c.AgentWait(context.Background(), "worker", until, 1234*time.Millisecond); err != nil {
		t.Fatal(err)
	}
}
func TestTabGetDecodesPaneCountNoPanes(t *testing.T) {
	c := wireServer(t, func(req map[string]json.RawMessage) string {
		return reply(req, `"result":{"type":"tab_info","tab":{"tab_id":"w1:t2","workspace_id":"w1","number":2,"label":"Run","focused":false,"pane_count":2,"agent_status":"working"}}`)
	})
	tab, err := c.TabGet(context.Background(), "w1:t2")
	if err != nil {
		t.Fatal(err)
	}
	if tab.PaneCount != 2 || tab.TabID != "w1:t2" || tab.WorkspaceID != "w1" {
		t.Fatalf("tab %+v", tab)
	}
}
func TestCallRejectsMalformedEnvelope(t *testing.T) {
	for _, body := range []string{`"result":null`, `"unrelated":true`, `"error":{},"result":{}`, `"result":{`} {
		t.Run(body, func(t *testing.T) {
			c := wireServer(t, func(req map[string]json.RawMessage) string { return reply(req, body) })
			if err := c.Call(context.Background(), "ping", nil, nil); err == nil {
				t.Fatal("accepted malformed envelope")
			}
		})
	}
	t.Run("wrong-id", func(t *testing.T) {
		c := wireServer(t, func(_ map[string]json.RawMessage) string { return "{\"id\":\"other\",\"result\":{}}\n" })
		if err := c.Call(context.Background(), "ping", nil, nil); err == nil {
			t.Fatal("accepted wrong id")
		}
	})
}
func TestCallCancellation(t *testing.T) {
	path := herdrtest.SocketPath(t)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	entered := make(chan struct{})
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		bufio.NewReader(c).ReadString('\n')
		close(entered)
		var b [1]byte
		c.Read(b[:])
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- (&herdr.Client{Path: path}).Call(ctx, "ping", nil, nil) }()
	<-entered
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("call ignored cancellation")
	}
	<-closed
}
func TestReadServerIDRejectsNonSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := herdr.ReadServerID(path); err == nil {
		t.Fatal("regular file accepted")
	}
	c := wireServer(t, func(req map[string]json.RawMessage) string { return reply(req, `"result":{}`) })
	link := filepath.Join(filepath.Dir(path), "link")
	if err := os.Symlink(c.Path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := herdr.ReadServerID(link); err == nil {
		t.Fatal("symlink accepted")
	}
}

// JSON string transport preserves every valid Unicode string, including line
// breaks and quotes, without creating extra wire requests.
func FuzzCallTextRoundTrip(f *testing.F) {
	f.Add("line\n\"quoted\"\\text")
	f.Add("")
	f.Add("雪\x00")
	f.Fuzz(func(t *testing.T, input string) {
		input = strings.ToValidUTF8(input, "�")
		c := wireServer(t, func(req map[string]json.RawMessage) string {
			var params struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(req["params"], &params); err != nil {
				t.Error(err)
			}
			if params.Text != input {
				t.Errorf("text changed: %q want %q", params.Text, input)
			}
			b, _ := json.Marshal(params)
			return reply(req, `"result":`+string(b))
		})
		var got struct {
			Text string `json:"text"`
		}
		if err := c.Call(context.Background(), "echo", map[string]string{"text": input}, &got); err != nil {
			t.Fatal(err)
		}
		if got.Text != input {
			t.Fatalf("round trip changed %q to %q", input, got.Text)
		}
	})
}

func TestTabCreateWithoutEnvOmitsNull(t *testing.T) {
	c := wireServer(t, func(req map[string]json.RawMessage) string {
		var params map[string]json.RawMessage
		json.Unmarshal(req["params"], &params)
		if env, ok := params["env"]; ok && string(env) == "null" {
			t.Error("env must be an object or omitted, not null")
		}
		if string(params["focus"]) != "false" {
			t.Error("tab creation must not focus")
		}
		return reply(req, `"result":{"type":"tab_created","tab":{"tab_id":"w1:t1"},"root_pane":{"pane_id":"w1:p1"}}`)
	})
	if _, _, err := c.TabCreate(context.Background(), "w1", "Run", "/tmp", nil); err != nil {
		t.Fatal(err)
	}
}

func TestSessionNamedHerdr(t *testing.T) {
	if got := herdr.SessionFromSocket("/tmp/sessions/herdr/herdr.sock"); got != "herdr" {
		t.Fatalf("session %q", got)
	}
}

func TestCallRejectsUnterminatedAndOversizeFrames(t *testing.T) {
	for _, body := range []string{strings.Repeat(" ", 4<<20) + "\n", `{"id":"1","result":{}}`} {
		t.Run(fmt.Sprint(len(body)), func(t *testing.T) {
			c := wireServer(t, func(_ map[string]json.RawMessage) string { return body })
			if err := c.Call(context.Background(), "ping", nil, nil); err == nil {
				t.Fatal("invalid frame accepted")
			}
		})
	}
}
func FuzzAgentSequenceDecode(f *testing.F) {
	f.Add(uint64(0))
	f.Add(^uint64(0))
	f.Fuzz(func(t *testing.T, seq uint64) {
		c := wireServer(t, func(req map[string]json.RawMessage) string {
			return reply(req, fmt.Sprintf(`"result":{"type":"agent_info","agent":{"completion_seq":%d,"agent_status":"idle"}}`, seq))
		})
		a, err := c.AgentGet(context.Background(), "worker")
		if err != nil {
			t.Fatal(err)
		}
		if a.CompletionSeq == nil || *a.CompletionSeq != seq {
			t.Fatalf("sequence %v want %d", a.CompletionSeq, seq)
		}
	})
}

func TestAgentWaitTimeoutBoundaries(t *testing.T) {
	for _, tt := range []struct {
		timeout time.Duration
		want    string
	}{
		{0, ""}, {time.Nanosecond, "1"}, {time.Duration(1<<63 - 1), "9223372036855"},
	} {
		t.Run(fmt.Sprint(tt.timeout), func(t *testing.T) {
			c := wireServer(t, func(req map[string]json.RawMessage) string {
				var p map[string]json.RawMessage
				json.Unmarshal(req["params"], &p)
				if string(p["timeout_ms"]) != tt.want {
					t.Errorf("timeout %s want %s", p["timeout_ms"], tt.want)
				}
				return reply(req, `"result":{"type":"agent_info","agent":{"agent_status":"idle"}}`)
			})
			if _, err := c.AgentWait(context.Background(), "worker", nil, tt.timeout); err != nil {
				t.Fatal(err)
			}
		})
	}
	c := &herdr.Client{Path: "/nonexistent.sock"}
	if _, err := c.AgentWait(context.Background(), "worker", nil, -time.Nanosecond); err == nil || !strings.Contains(err.Error(), "negative") {
		t.Fatal(err)
	}
}
func TestAgentRejectsInvalidSequence(t *testing.T) {
	for _, seq := range []string{"-1", "0.5", "18446744073709551616", `"0"`} {
		t.Run(seq, func(t *testing.T) {
			c := wireServer(t, func(req map[string]json.RawMessage) string {
				return reply(req, `"result":{"type":"agent_info","agent":{"completion_seq":`+seq+`}}`)
			})
			if _, err := c.AgentGet(context.Background(), "worker"); err == nil {
				t.Fatal("invalid sequence accepted")
			}
		})
	}
}
func TestHelpersRejectWrongPayload(t *testing.T) {
	for _, body := range []string{`"type":"tab_info","agent":{}`, `"type":"agent_info"`, `"type":"agent_info","agent":null`} {
		t.Run(body, func(t *testing.T) {
			c := wireServer(t, func(req map[string]json.RawMessage) string { return reply(req, `"result":{`+body+`}`) })
			if _, err := c.AgentGet(context.Background(), "worker"); err == nil {
				t.Fatal("wrong payload accepted")
			}
		})
	}
}

func TestPingRejectsMissingRequiredFields(t *testing.T) {
	for _, fields := range []string{``, `,"version":"0.9.1"`, `,"protocol":22`, `,"version":null,"protocol":22`, `,"version":"0.9.1","protocol":null`, `,"version":"","protocol":22`} {
		t.Run(fields, func(t *testing.T) {
			c := wireServer(t, func(req map[string]json.RawMessage) string { return reply(req, `"result":{"type":"pong"`+fields+`}`) })
			if _, err := c.Ping(context.Background()); err == nil {
				t.Fatal("incomplete pong accepted")
			}
		})
	}
}
func TestPingPreservesPresentZeroProtocol(t *testing.T) {
	// Zero is within the schema's uint32 domain; callers decide compatibility.
	c := wireServer(t, func(req map[string]json.RawMessage) string {
		return reply(req, `"result":{"type":"pong","version":"example","protocol":0}`)
	})
	p, err := c.Ping(context.Background())
	if err != nil || p.Version != "example" || p.Protocol != 0 {
		t.Fatalf("pong %+v %v", p, err)
	}
}

func TestPaneReportMetadataOmitsNilTokens(t *testing.T) {
	c := wireServer(t, func(req map[string]json.RawMessage) string {
		var params map[string]json.RawMessage
		json.Unmarshal(req["params"], &params)
		if tokens, ok := params["tokens"]; ok && string(tokens) == "null" {
			t.Error("tokens must be an object or omitted, not null")
		}
		if string(params["source"]) != `"herdr-kata"` || string(params["pane_id"]) != `"w1:p1"` {
			t.Errorf("metadata params: %s", req["params"])
		}
		return reply(req, `"result":{"type":"ok"}`)
	})
	if err := c.PaneReportMetadata(context.Background(), "w1:p1", nil); err != nil {
		t.Fatal(err)
	}
}
func TestCallPayloadSizeBoundary(t *testing.T) {
	const maxPayload = 4 << 20
	for _, size := range []int{maxPayload - 1, maxPayload, maxPayload + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			wantLength := make(chan int, 1)
			c := wireServer(t, func(req map[string]json.RawMessage) string {
				prefix := fmt.Sprintf(`{"id":%s,"result":{"padding":"`, req["id"])
				suffix := `"}}`
				paddingLength := size - len(prefix) - len(suffix)
				wantLength <- paddingLength
				return prefix + strings.Repeat("x", paddingLength) + suffix + "\n"
			})
			var got struct {
				Padding string `json:"padding"`
			}
			err := c.Call(context.Background(), "echo", nil, &got)
			if size > maxPayload {
				if err == nil {
					t.Fatal("oversize payload accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if expected := <-wantLength; len(got.Padding) != expected {
				t.Fatalf("padding length %d want %d", len(got.Padding), expected)
			}
		})
	}
}

func TestSocketPathWithoutHomeFailsClosed(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	t.Setenv("HERDR_SOCKET_PATH", "")
	path := herdr.SocketPath("build")
	if path != "" {
		t.Fatalf("missing home resolved to a relative socket: %q", path)
	}
	_, err := (&herdr.Client{Path: path}).Ping(context.Background())
	if err == nil || !strings.Contains(err.Error(), "HOME") {
		t.Fatalf("missing socket configuration not diagnosed: %v", err)
	}
	t.Setenv("HERDR_SOCKET_PATH", "/tmp/explicit.sock")
	if got := herdr.SocketPath("build"); got != "/tmp/explicit.sock" {
		t.Fatalf("explicit path ignored without HOME: %q", got)
	}
}

func TestSocketPathUsesXDGConfigHome(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", "")
	t.Setenv("XDG_CONFIG_HOME", "/srv/config")
	if got := herdr.SocketPath("work"); got != "/srv/config/herdr/sessions/work/herdr.sock" {
		t.Fatal(got)
	}
	t.Setenv("HERDR_SOCKET_PATH", "/tmp/override.sock")
	if got := herdr.SocketPath("work"); got != "/tmp/override.sock" {
		t.Fatal(got)
	}
}
