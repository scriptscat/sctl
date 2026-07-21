// Package sctl exposes assets shared by every sctl subcommand.
package sctl

import _ "embed"

// ProtocolJSON is the canonical bridge protocol constants file. The authoritative
// copy lives in the scriptcat extension repo; CI diffs this mirror byte-for-byte.
//
//go:embed protocol.json
var ProtocolJSON []byte
