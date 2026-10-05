package store

import (
	"hegel.dev/go/hegel"
	"net/url"
	"strings"
	"testing"
)

func TestLegacySQLiteURIUsesEmptyAuthority(t *testing.T) {
	for _, tc := range []struct{ path, want string }{{"C:/x/y.db", "file:///C:/x/y.db?mode=ro&immutable=1"}, {"C:/snapshot/", "file:///C:/snapshot/?mode=ro&immutable=1"}, {"/x/y.db", "file:///x/y.db?mode=ro&immutable=1"}, {"C:/x/a #?%.db", "file:///C:/x/a%20%23%3F%25.db?mode=ro&immutable=1"}} {
		uri := legacySQLiteURI(tc.path)
		if uri != tc.want {
			t.Errorf("path %q URI=%q want=%q", tc.path, uri, tc.want)
		}
		parsed, err := url.Parse(uri)
		if err != nil || parsed.Host != "" || parsed.Scheme != "file" {
			t.Errorf("drive interpreted as authority: %+v %v", parsed, err)
		}
	}
}
func TestLegacySQLiteURIPathEscapesFullText(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		text := hegel.Draw(ht, hegel.Text())
		for _, prefix := range []string{"/snapshot/", "C:/snapshot/"} {
			path := prefix + text
			parsed, err := url.Parse(legacySQLiteURI(path))
			if err != nil {
				ht.Fatal(err)
			}
			want := path
			if !strings.HasPrefix(want, "/") {
				want = "/" + want
			}
			if parsed.Scheme != "file" || parsed.Host != "" || parsed.Path != want || parsed.Fragment != "" || parsed.Query().Get("mode") != "ro" || parsed.Query().Get("immutable") != "1" || len(parsed.Query()) != 2 {
				ht.Fatalf("path did not roundtrip with readonly options: path=%q parsed=%+v", path, parsed)
			}
		}
	})
}
