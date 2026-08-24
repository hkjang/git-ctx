package version

import (
	"runtime/debug"
	"strings"
)

// Version is the released version of this build. It is a constant so an
// offline build without git metadata still reports something meaningful.
const Version = "0.45.0"

// Commit and BuildTime are injected at build time with -ldflags. They answer
// the question a version string alone cannot: "is the container actually
// running the build I just deployed, or the previous one with the same tag?"
var (
	Commit    = ""
	BuildTime = ""
)

// Build describes the running binary for the status screens and the startup
// log. The commit falls back to the VCS stamp the toolchain records, so a
// plain `go build` still identifies itself.
func Build() (commit string, buildTime string, modified bool) {
	commit, buildTime = Commit, BuildTime
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return commit, buildTime, false
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			if commit == "" {
				commit = setting.Value
			}
		case "vcs.time":
			if buildTime == "" {
				buildTime = setting.Value
			}
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	return commit, buildTime, modified
}

// Full renders version and build together, for a log line or a tooltip.
func Full() string {
	commit, buildTime, modified := Build()
	parts := []string{Version}
	if commit != "" {
		short := commit
		if len(short) > 12 {
			short = short[:12]
		}
		if modified {
			short += "+dirty"
		}
		parts = append(parts, short)
	}
	if buildTime != "" {
		parts = append(parts, buildTime)
	}
	return strings.Join(parts, " · ")
}
