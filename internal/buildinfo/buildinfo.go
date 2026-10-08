// Package buildinfo carries the version and commit baked in at build time.
//
// Set them with -ldflags, e.g.
//
//	-X github.com/chinny/goldberry/internal/buildinfo.Version=1.2.3
//	-X github.com/chinny/goldberry/internal/buildinfo.Commit=<sha>
//
// When unset, Commit falls back to the VCS revision Go embeds in the binary.
package buildinfo

import "runtime/debug"

// Repo is the public source repository. Every page links to it (AGPL-3.0 §13).
const Repo = "https://github.com/chinny/goldberry"

var (
	Version = "dev"
	Commit  = ""
)

func init() {
	if Commit != "" {
		return
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" {
				Commit = s.Value
			}
		}
	}
}

// ShortCommit is the first 12 characters of Commit, or "unknown".
func ShortCommit() string {
	switch {
	case Commit == "":
		return "unknown"
	case len(Commit) > 12:
		return Commit[:12]
	default:
		return Commit
	}
}

// SourceURL links to the exact source of the running build.
func SourceURL() string {
	if Commit == "" {
		return Repo
	}
	return Repo + "/tree/" + Commit
}
