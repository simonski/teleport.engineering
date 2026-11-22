// Package version defines the independent CLI and server application versions.
package version

import (
	"regexp"
	"strconv"
)

const (
	CLI    = "1.1.0"
	Server = "1.1.0"

	// Header advertises the server application version on every response.
	Header = "X-Teleport-Version"
)

// BuildNumber identifies a local build separately from its application versions.
var BuildNumber = "0"

var advertised = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:[-+][A-Za-z0-9.+-]+)?$`)

// MajorMinor accepts the older prerelease/build suffixes on observed versions.
func MajorMinor(value string) (major int, minor int, ok bool) {
	if len(value) > 128 {
		return 0, 0, false
	}
	parts := advertised.FindStringSubmatch(value)
	if parts == nil {
		return 0, 0, false
	}
	major, first := strconv.Atoi(parts[1])
	minor, second := strconv.Atoi(parts[2])
	return major, minor, first == nil && second == nil
}
