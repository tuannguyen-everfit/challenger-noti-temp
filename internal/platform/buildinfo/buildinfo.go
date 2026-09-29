// Package buildinfo exposes the release version embedded at compile time from
// the adjacent VERSION file. This is the single source of truth — bump VERSION,
// commit, tag, push.
package buildinfo

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var rawVersion string

// Version returns the trimmed contents of internal/buildinfo/VERSION.
// ldflags (-X main.version=...) still take precedence over this when set.
func Version() string {
	return strings.TrimSpace(rawVersion)
}
