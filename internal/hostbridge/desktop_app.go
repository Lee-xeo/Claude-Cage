package hostbridge

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// DesktopApp is a vendor's graphical application. The application installs
// it in the environment from the vendor's own source, keeps it at the latest
// release and shows it in a pane of its own. The plugin only describes where
// the release comes from and how to start it: every step that needs the
// administrator account is the application's, never a program of the plugin.
type DesktopApp struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Icon  string `json:"icon,omitempty"`
	// Source is where the latest release comes from.
	Source AppSource `json:"source"`
	// Command starts the application. For apt and deb sources the program is
	// an absolute path the package installs; for an archive it is a path
	// inside the unpacked release.
	Command []string `json:"command"`
	// Schemes are the link schemes the vendor's sign-in returns to. Links of
	// these schemes opened in the browser are handed to the application.
	Schemes []string `json:"schemes,omitempty"`
	// Packages are Debian packages the application needs beside its own.
	Packages []string `json:"packages,omitempty"`
}

// Kinds of application source.
const (
	// SourceApt is the vendor's signed APT repository.
	SourceApt = "apt"
	// SourceDeb is a Debian package the vendor publishes for download.
	SourceDeb = "deb"
	// SourceArchive is a .tar.gz or .tar.xz release the vendor publishes.
	SourceArchive = "archive"
)

// AppSource names the vendor's distribution. A plugin never names a version:
// the latest release is always the one installed.
type AppSource struct {
	Type string `json:"type"`

	// For apt: the repository, its signing key and the package.
	Repository string   `json:"repository,omitempty"`
	Suite      string   `json:"suite,omitempty"`
	Components []string `json:"components,omitempty"`
	Key        string   `json:"key,omitempty"`
	// Fingerprint is the signing key's full fingerprint, 40 hexadecimal
	// digits. A downloaded key that does not match stops the installation.
	Fingerprint string `json:"fingerprint,omitempty"`
	Package     string `json:"package,omitempty"`

	// For deb and archive: Resolve is a self-contained .py or .sh program
	// inside the plugin. It runs as the environment's ordinary user with the
	// processor (amd64 or arm64) as its argument and prints the latest
	// release as one JSON object:
	// {"version": "...", "url": "https://...", "sha256": "..."}. The address
	// must be https to one of Hosts. sha256 is optional when the vendor
	// publishes none.
	Resolve []string `json:"resolve,omitempty"`
	Hosts   []string `json:"hosts,omitempty"`
}

// desktopAppsSince is the first application version that runs desktop
// applications. Older versions reject the field as unknown.
var desktopAppsSince = [3]int{0, 3, 2}

var (
	debianPackage = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]{1,62}$`)
	aptWord       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,62}$`)
	keyPrint      = regexp.MustCompile(`^[0-9A-F]{40}$`)
	// The application's own handlers and the system's.
	reservedAppSchemes = []string{"claude", "tailscale", "x-scheme-handler"}
)

// validHTTPS reports whether address is https to a named host on the default
// port, without user information, a query or a fragment.
func validHTTPS(address string) bool {
	rest, found := strings.CutPrefix(address, "https://")
	host, _, _ := strings.Cut(rest, "/")
	return found && len(address) <= 400 && hostName.MatchString(host) && !strings.ContainsAny(rest, " \t\r\n?#@\\")
}

// validArgument reports whether a command argument is plain text.
func validArgument(value string) bool {
	return value != "" && len(value) <= 200 && !strings.ContainsAny(value, "\x00\r\n")
}

func (s AppSource) check(id string) error {
	switch s.Type {
	case SourceApt:
		switch {
		case !validHTTPS(s.Repository):
			return fmt.Errorf("desktopApps %s: source.repository must be an https address", id)
		case !aptWord.MatchString(s.Suite):
			return fmt.Errorf("desktopApps %s: source.suite is missing or not valid", id)
		case len(s.Components) == 0 || len(s.Components) > 4:
			return fmt.Errorf("desktopApps %s: source.components must list one to four components", id)
		case !validHTTPS(s.Key):
			return fmt.Errorf("desktopApps %s: source.key must be an https address", id)
		case !keyPrint.MatchString(s.Fingerprint):
			return fmt.Errorf("desktopApps %s: source.fingerprint must be 40 uppercase hexadecimal digits", id)
		case !debianPackage.MatchString(s.Package):
			return fmt.Errorf("desktopApps %s: source.package is not a Debian package name", id)
		case len(s.Resolve) > 0 || len(s.Hosts) > 0:
			return fmt.Errorf("desktopApps %s: resolve and hosts are for deb and archive sources", id)
		}
		for _, component := range s.Components {
			if !aptWord.MatchString(component) || strings.Contains(component, "/") {
				return fmt.Errorf("desktopApps %s: source component %q is not valid", id, component)
			}
		}
	case SourceDeb, SourceArchive:
		switch {
		case s.Repository != "" || s.Suite != "" || len(s.Components) > 0 || s.Key != "" || s.Fingerprint != "" || s.Package != "":
			return fmt.Errorf("desktopApps %s: repository fields are for apt sources", id)
		case len(s.Resolve) == 0 || !validFile(s.Resolve[0], ".py", ".sh"):
			return fmt.Errorf("desktopApps %s: source.resolve must start with a self-contained .py or .sh file inside the plugin", id)
		case len(s.Resolve) > 8:
			return fmt.Errorf("desktopApps %s: source.resolve has too many arguments", id)
		case len(s.Hosts) == 0 || len(s.Hosts) > 8:
			return fmt.Errorf("desktopApps %s: source.hosts must list one to eight download hosts", id)
		}
		for _, argument := range s.Resolve[1:] {
			if !validArgument(argument) {
				return fmt.Errorf("desktopApps %s: source.resolve argument is not valid", id)
			}
		}
		for _, host := range s.Hosts {
			if !hostName.MatchString(host) {
				return fmt.Errorf("desktopApps %s: source host %q must be a full host name", id, host)
			}
		}
	default:
		return fmt.Errorf("desktopApps %s: source.type must be apt, deb or archive", id)
	}
	return nil
}

func (a DesktopApp) check(schemes []string) error {
	switch {
	case !validID(a.ID):
		return fmt.Errorf("desktopApps: id %q is not valid", a.ID)
	case strings.TrimSpace(a.Title) == "" || len(a.Title) > 80:
		return fmt.Errorf("desktopApps %s: title is missing or too long", a.ID)
	case a.Icon != "" && !validIcon(a.Icon):
		return fmt.Errorf("desktopApps %s: icon must be a PNG or SVG file inside the plugin", a.ID)
	case len(a.Command) == 0 || len(a.Command) > 16:
		return fmt.Errorf("desktopApps %s: command is missing or too long", a.ID)
	case len(a.Schemes) > 4:
		return fmt.Errorf("desktopApps %s: at most four schemes", a.ID)
	case len(a.Packages) > 40:
		return fmt.Errorf("desktopApps %s: at most forty packages", a.ID)
	}
	if err := a.Source.check(a.ID); err != nil {
		return err
	}
	program := a.Command[0]
	if a.Source.Type == SourceArchive {
		if !validRelative(program) {
			return fmt.Errorf("desktopApps %s: command must start with a program inside the release", a.ID)
		}
	} else if !strings.HasPrefix(program, "/usr/") && !strings.HasPrefix(program, "/opt/") || strings.Contains(program, "/../") || strings.HasSuffix(program, "/") || !validArgument(program) {
		return fmt.Errorf("desktopApps %s: command must start with an absolute path under /usr or /opt", a.ID)
	}
	for _, argument := range a.Command[1:] {
		if !validArgument(argument) {
			return fmt.Errorf("desktopApps %s: command argument is not valid", a.ID)
		}
	}
	for i, scheme := range a.Schemes {
		switch {
		case !uriScheme.MatchString(scheme) || len(scheme) > 40 || slices.Contains(commonSchemes, scheme) || slices.Contains(reservedAppSchemes, scheme):
			return fmt.Errorf("desktopApps %s: scheme %q is not valid or is reserved", a.ID, scheme)
		case slices.Contains(a.Schemes[:i], scheme) || slices.Contains(schemes, scheme):
			return fmt.Errorf("desktopApps %s: scheme %q is repeated", a.ID, scheme)
		}
	}
	for _, name := range a.Packages {
		if !debianPackage.MatchString(name) {
			return fmt.Errorf("desktopApps %s: package %q is not a Debian package name", a.ID, name)
		}
	}
	return nil
}

// checkDesktopApps validates the desktop applications of a guest plugin.
// schemes are the ones its link handlers already take.
func (m *Manifest) checkDesktopApps(schemes []string) error {
	apps := m.Contributes.DesktopApps
	if len(apps) == 0 {
		return nil
	}
	switch {
	case m.Kind != KindGuest:
		return fmt.Errorf("desktopApps are for guest plugins")
	case len(apps) > 4:
		return fmt.Errorf("at most four desktopApps")
	}
	if floor, ok := engineFloor(m.Engines.Cage); !ok || slices.Compare(floor[:], desktopAppsSince[:]) < 0 {
		return fmt.Errorf("desktopApps need engines.cage of at least %d.%d.%d", desktopAppsSince[0], desktopAppsSince[1], desktopAppsSince[2])
	}
	var ids []string
	taken := slices.Clone(schemes)
	for _, app := range apps {
		if slices.Contains(ids, app.ID) {
			return fmt.Errorf("desktopApps: id %q is repeated", app.ID)
		}
		if err := app.check(taken); err != nil {
			return err
		}
		ids = append(ids, app.ID)
		taken = append(taken, app.Schemes...)
	}
	return nil
}

// AppSchemes are the link schemes the plugin's desktop applications take.
func (m Manifest) AppSchemes() []string {
	if m.Contributes == nil {
		return nil
	}
	var schemes []string
	for _, app := range m.Contributes.DesktopApps {
		schemes = append(schemes, app.Schemes...)
	}
	return schemes
}
