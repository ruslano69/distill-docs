// Package version carries the build version, injected at link time via
// -ldflags "-X github.com/ruslano69/distill-docs/internal/version.Version=...".
package version

import "fmt"

// Version is the build version; "dev" for unversioned local builds.
var Version = "dev"

// PrintVersion writes "<tool> version <Version>" to stdout.
func PrintVersion(tool string) {
	fmt.Printf("%s version %s\n", tool, Version)
}
