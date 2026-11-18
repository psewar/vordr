package protocol

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Bounds of the policy summary an agent's hello carries that agent.yaml decides. The hub drops a field beyond them
// (ADR-0191), and check-config refuses an agent.yaml that would go beyond them (ADR-0195): both read them here, so that
// the two cannot drift apart.
const (
	MaxApps          = 64  // entries of apps
	MaxAppNameLen    = 255 // bytes of an app's name, as of a fact of the hello
	MaxAllowedScopes = 128 // entries of install.allowed_scopes
	MaxWindowDays    = 64  // entries in the days of a window; seven different ones are all there are
)

// MaxInventoryLocations bounds the locations of one inventory: the agent reports no more, and the hub keeps no more of
// one (ADR-0195). agent.yaml names up to 50 patterns, each of which may match many project folders.
const MaxInventoryLocations = 1024

// PlainText reports whether s is valid UTF-8 without control characters: text that can be shown to people as it is,
// in Home Assistant and on a terminal.
func PlainText(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}
