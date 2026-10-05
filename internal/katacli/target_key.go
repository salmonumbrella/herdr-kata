package katacli

import "encoding/json"

// LocalTargetKey identifies explicit local routing, excluding rotating credentials
// and attribution, which are checked separately by the caller.
func LocalTargetKey(t Target) string {
	raw, _ := json.Marshal(struct {
		Server, Daemon, Home, Project, Workspace string
		TrustPrivateNetwork                      bool
	}{t.Server, t.Daemon, t.Home, t.Project, t.Workspace, t.TrustPrivateNetwork})
	return string(raw)
}
