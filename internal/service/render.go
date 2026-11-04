// Package service manages optional per-installation login launchers.
package service

import (
	"bytes"
	"context"
	"embed"
	"encoding/xml"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"
	"unicode"
	"unicode/utf8"

	"github.com/salmonumbrella/herdr-kata/internal/control"
)

type Manager struct {
	OS, Home, UID string
	Getenv        func(string) string
	Run           func(context.Context, string, ...string) error
}

//go:embed *.tmpl
var files embed.FS

var ErrInput = errors.New("invalid service input")
var templates = template.Must(template.New("").Funcs(template.FuncMap{
	"xml": func(v string) string {
		var b strings.Builder
		_ = xml.EscapeText(&b, []byte(v))
		return b.String()
	},
	"quote": func(v string) string { return strconv.Quote(strings.ReplaceAll(v, "%", "%%")) },
}).ParseFS(files, "*.tmpl"))

func valid(v string) bool {
	return utf8.ValidString(v) && strings.IndexFunc(v, func(r rune) bool { return unicode.IsControl(r) || r >= 0xfdd0 && r <= 0xfdef || r&0xffff >= 0xfffe }) < 0
}

func (m Manager) render(configPath, root, executable string) ([]byte, error) {
	if m.OS != "darwin" && m.OS != "linux" {
		return nil, fmt.Errorf("service: unsupported on %s; run herdr-kata up from your login environment", m.OS)
	}
	if m.OS == "linux" && strings.ContainsAny(executable, "'\"\\") {
		return nil, fmt.Errorf("%w: systemd executable path cannot contain quotes or backslashes; move the binary and reinstall", ErrInput)
	}
	env := map[string]string{}
	paths := []string{m.Home, configPath, root, executable}
	for _, key := range []string{"XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"} {
		if v := m.Getenv(key); v != "" {
			env[key] = v
			paths = append(paths, v)
		}
	}
	for _, path := range paths {
		if !filepath.IsAbs(path) || !valid(path) {
			return nil, fmt.Errorf("%w: paths must be absolute and printable UTF-8", ErrInput)
		}
	}
	path := m.Getenv("PATH")
	for dir := range strings.SplitSeq(path, ":") {
		if filepath.IsAbs(dir) {
			env["PATH"] += dir + ":"
		}
	}
	env["PATH"] = strings.TrimSuffix(env["PATH"], ":")
	if !valid(path) || env["PATH"] == "" {
		return nil, fmt.Errorf("%w: PATH needs printable absolute entries", ErrInput)
	}
	var b bytes.Buffer
	err := templates.ExecuteTemplate(&b, map[string]string{"darwin": "launchd.tmpl", "linux": "systemd.tmpl"}[m.OS], struct {
		Name, Home string
		Argv       []string
		Env        map[string]string
	}{control.Name(root), m.Home, []string{executable, "--config", configPath, "--state", root, "up"}, env})
	return b.Bytes(), err
}
