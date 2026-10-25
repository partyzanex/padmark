// Package buildinfo identifies the application release.
package buildinfo

import "strings"

// version is injected by release builds; source builds use the current release draft.
var version string

// Version returns the release version without the Git tag's optional v prefix.
func Version() string {
	if version != "" {
		return strings.TrimPrefix(version, "v")
	}

	return "0.4.0"
}
