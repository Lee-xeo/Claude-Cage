package version

import "runtime/debug"

// Build describes this executable, independent of any checkout on the host.
type Build struct {
	Version  string `json:"version"`
	Commit   string `json:"commit,omitempty"`
	Modified bool   `json:"modified"`
}

func CurrentBuild() Build {
	b := Build{Version: Version}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				b.Commit = setting.Value
			case "vcs.modified":
				b.Modified = setting.Value == "true"
			}
		}
	}
	return b
}
