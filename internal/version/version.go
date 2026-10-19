// Package version is the sbx release stamped into the binary.
package version

import "fmt"

// Version, Commit, and Date are variables so a release build can override
// them with -ldflags -X. Development builds keep the defaults.
//
//	-X github.com/TorstenDittmann/sbx/internal/version.Version=<version>
//	-X github.com/TorstenDittmann/sbx/internal/version.Commit=<commit>
//	-X github.com/TorstenDittmann/sbx/internal/version.Date=<time>
var (
	Version = "0.2.0"
	Commit  = ""
	Date    = ""
)

// Format is the text printed by `sbx version`.
func Format() string {
	return FormatBinary("sbx")
}

// FormatBinary is the version text for a binary that shares this stamp.
func FormatBinary(name string) string {
	text := fmt.Sprintf("%s %s\n", name, Version)
	if Commit != "" {
		text += fmt.Sprintf("commit %s\n", Commit)
	}
	if Date != "" {
		text += fmt.Sprintf("built %s\n", Date)
	}
	return text
}
