package usage

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Discovery and wire fields follow agentsview's config, daemon_runtime,
// apiclient and session_usage commands. No daemon is started or refreshed here.
func agentsviewEndpoint() (string, string) {
	home, _ := os.UserHomeDir()
	dir := os.Getenv("AGENTSVIEW_DATA_DIR")
	if dir == "" {
		dir = os.Getenv("AGENT_VIEWER_DATA_DIR")
	}
	if dir == "" {
		dir = filepath.Join(home, ".agentsview")
	}
	if strings.HasPrefix(dir, "~/") {
		dir = filepath.Join(home, dir[2:])
	}
	var cfg struct {
		Host      string
		Port      int
		AuthToken string `toml:"auth_token"`
		PublicURL string `toml:"public_url"`
	}
	cfg.Host, cfg.Port = "127.0.0.1", 8080
	_, configErr := toml.DecodeFile(filepath.Join(dir, "config.toml"), &cfg)
	token := os.Getenv("AGENTSVIEW_AUTH_TOKEN")
	if token == "" {
		token = cfg.AuthToken
	}
	basePath := ""
	if u, err := url.Parse(cfg.PublicURL); err == nil {
		basePath = strings.TrimRight(u.EscapedPath(), "/")
	}
	records, _ := filepath.Glob(filepath.Join(dir, "daemon.*.json"))
	var newest time.Time
	found := false
	for _, p := range records {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var r struct {
			Service, Network, Address string
			StartedAt                 time.Time `json:"started_at"`
			Metadata                  map[string]string
		}
		if json.Unmarshal(b, &r) != nil || r.Service != "agentsview" || r.Network != "" && r.Network != "tcp" {
			continue
		}
		if found && r.StartedAt.Before(newest) {
			continue
		}
		host, port, err := net.SplitHostPort(r.Address)
		if err != nil {
			continue
		}
		n, err := strconv.Atoi(port)
		if err != nil {
			continue
		}
		cfg.Host, cfg.Port = host, n
		if h := r.Metadata["host"]; h != "" {
			cfg.Host = h
		}
		if p, err := strconv.Atoi(r.Metadata["port"]); err == nil {
			cfg.Port = p
		}
		if u, err := url.Parse(r.Metadata["browser_url"]); err == nil {
			basePath = strings.TrimRight(u.EscapedPath(), "/")
		}
		newest, found = r.StartedAt, true
	}
	if !found && configErr != nil {
		return "", ""
	}
	switch cfg.Host {
	case "", "0.0.0.0":
		cfg.Host = "127.0.0.1"
	case "::":
		cfg.Host = "::1"
	}
	ip := net.ParseIP(cfg.Host)
	if cfg.Host != "localhost" && (ip == nil || !ip.IsLoopback()) || cfg.Port <= 0 || cfg.Port > 65535 {
		return "", ""
	}
	return "http://" + net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)) + basePath, token
}
func agentsviewUsage(ctx context.Context, req Request) Usage {
	if req.Session == nil || req.Session.Value == "" {
		return Usage{}
	}
	base, token := agentsviewEndpoint()
	if base == "" {
		return Usage{}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	id := req.Session.Value
	var resp *http.Response
	for {
		request, err := http.NewRequestWithContext(ctx, "GET", base+"/api/v1/sessions/"+url.PathEscape(id)+"/usage?breakdown=true", nil)
		if err != nil {
			return Usage{}
		}
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err = client.Do(request)
		if err != nil {
			return Usage{}
		}
		// Like the CLI resolver, try Codex's archive prefix after an exact miss.
		if resp.StatusCode == http.StatusNotFound && req.Session.Agent == "codex" && !strings.HasPrefix(id, "codex:") {
			resp.Body.Close()
			id = "codex:" + id
			continue
		}
		break
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Usage{}
	}
	type cost struct{ Microdollars int64 }
	var wire struct {
		HasTokenData      bool `json:"has_token_data"`
		HasCost           bool `json:"has_cost"`
		Cost              cost
		TotalOutputTokens int64 `json:"total_output_tokens"`
		Breakdown         []struct {
			Timestamp string
			Input     int64 `json:"input_tokens"`
			Output    int64 `json:"output_tokens"`
			Read      int64 `json:"cache_read_input_tokens"`
			Write     int64 `json:"cache_creation_input_tokens"`
			HasCost   bool  `json:"has_cost"`
			Cost      cost
		}
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&wire) != nil {
		return Usage{}
	}
	u := Usage{Source: "agentsview"}
	selected, excluded := 0, false
	allCosts := true
	var totalCost int64
	for _, row := range wire.Breakdown {
		at, err := time.Parse(time.RFC3339Nano, row.Timestamp)
		if err != nil || !req.StartedAt.IsZero() && at.Before(req.StartedAt) || !req.EndedAt.IsZero() && at.After(req.EndedAt) {
			excluded = true
			continue
		}
		selected++
		u.InputTokens += max(0, row.Input)
		u.OutputTokens += max(0, row.Output)
		u.CacheReadTokens += max(0, row.Read)
		u.CacheCreationTokens += max(0, row.Write)
		allCosts = allCosts && row.HasCost
		totalCost += max(0, row.Cost.Microdollars)
	}
	u.Found = wire.HasTokenData && selected > 0
	if selected > 0 && allCosts {
		u.CostMicroUSD = &totalCost
	} else if wire.HasCost && !excluded {
		n := max(0, wire.Cost.Microdollars)
		u.CostMicroUSD = &n
	}
	return u
}
