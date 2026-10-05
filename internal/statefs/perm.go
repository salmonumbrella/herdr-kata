// Package statefs holds the permission bits every file and directory herdr-kata
// creates under its state directory is made with.
//
// The reason they are constants in one package rather than a literal at each
// call site: what herdr-kata writes there is not configuration. It is prompts,
// transcripts, execution leases and result files — the whole content
// of what agents were told to do and what they said back. On a machine with a
// second login, 0644 hands all of it to that login for the cost of a `cat`.
// Nothing herdr-kata writes is meant to be read by another user, and nothing it
// writes is executed, so the owner-only bits are the whole set it needs.
package statefs

const (
	// Dir is the mode for a directory herdr-kata creates. MkdirAll only applies
	// it to directories it actually creates, so pointing herdr-kata at an
	// existing directory — a vault someone else made — leaves that one alone.
	Dir = 0o700

	// File is the mode for a file herdr-kata creates.
	File = 0o600
)
