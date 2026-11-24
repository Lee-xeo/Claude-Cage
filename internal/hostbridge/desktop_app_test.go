package hostbridge

import (
	"strings"
	"testing"
)

const desktopAppsManifest = `{
  "name": "workshop",
  "version": "1.0.0",
  "description": "Runs two vendor applications.",
  "engines": { "cage": ">=0.3.2" },
  "kind": "guest",
  "permissions": [],
  "contributes": {
    "desktopApps": [
      {
        "id": "repo-app", "title": "Repo App", "icon": "icon.svg",
        "source": { "type": "apt", "repository": "https://downloads.example.com/apt", "suite": "stable", "components": ["main"],
          "key": "https://downloads.example.com/key.asc", "fingerprint": "31DDDE24DDFAB679F42D7BD2BAA929FF1A7ECACE", "package": "repo-app" },
        "command": ["/usr/bin/repo-app", "--no-sandbox-warning"],
        "schemes": ["repo-app"],
        "packages": ["libsecret-1-0"]
      },
      {
        "id": "tar-app", "title": "Tar App",
        "source": { "type": "archive", "resolve": ["resolve.py", "--stable"], "hosts": ["dl.example.com"] },
        "command": ["Tar App/tar-app"],
        "schemes": ["tar-app"]
      }
    ]
  }
}`

func TestManifestDesktopApps(t *testing.T) {
	m, err := parseManifest([]byte(desktopAppsManifest))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Contributes.DesktopApps) != 2 || strings.Join(m.AppSchemes(), ",") != "repo-app,tar-app" {
		t.Fatalf("manifest %+v", m.Contributes)
	}
	broken := map[string][2]string{
		"host plugin":            {`"kind": "guest"`, `"kind": "host"`},
		"older engine":           {`>=0.3.2`, `>=0.3.1`},
		"unknown field":          {`"title": "Tar App",`, `"title": "Tar App", "version": "1.2",`},
		"source type":            {`"type": "archive"`, `"type": "snap"`},
		"repository plain http":  {`https://downloads.example.com/apt`, `http://downloads.example.com/apt`},
		"repository query":       {`https://downloads.example.com/apt`, `https://downloads.example.com/apt?x=1`},
		"repository bare host":   {`https://downloads.example.com/apt`, `https://intranet/apt`},
		"key short fingerprint":  {`31DDDE24DDFAB679F42D7BD2BAA929FF1A7ECACE`, `1A7ECACE`},
		"key lowercase":          {`31DDDE24DDFAB679F42D7BD2BAA929FF1A7ECACE`, `31ddde24ddfab679f42d7bd2baa929ff1a7ecace`},
		"package name":           {`"package": "repo-app"`, `"package": "repo app"`},
		"no components":          {`"components": ["main"]`, `"components": []`},
		"component path":         {`"components": ["main"]`, `"components": ["main/../x"]`},
		"apt with resolve":       {`"package": "repo-app" }`, `"package": "repo-app", "resolve": ["x.py"] }`},
		"archive with key":       {`"hosts": ["dl.example.com"] }`, `"hosts": ["dl.example.com"], "key": "https://dl.example.com/k" }`},
		"resolve outside":        {`"resolve.py"`, `"../resolve.py"`},
		"resolve type":           {`"resolve.py"`, `"resolve.exe"`},
		"no hosts":               {`"hosts": ["dl.example.com"]`, `"hosts": []`},
		"host wildcard":          {`"hosts": ["dl.example.com"]`, `"hosts": ["*.example.com"]`},
		"relative apt program":   {`"/usr/bin/repo-app"`, `"usr/bin/repo-app"`},
		"apt program elsewhere":  {`"/usr/bin/repo-app"`, `"/home/user/repo-app"`},
		"apt program climbs":     {`"/usr/bin/repo-app"`, `"/usr/../tmp/repo-app"`},
		"archive program abs":    {`"Tar App/tar-app"`, `"/opt/tar-app"`},
		"archive program climbs": {`"Tar App/tar-app"`, `"../tar-app"`},
		"argument newline":       {`"--no-sandbox-warning"`, `"--a\nb"`},
		"scheme reserved":        {`"schemes": ["repo-app"]`, `"schemes": ["https"]`},
		"scheme of the app":      {`"schemes": ["repo-app"]`, `"schemes": ["claude"]`},
		"scheme form":            {`"schemes": ["repo-app"]`, `"schemes": ["Repo_App"]`},
		"scheme repeated":        {`"schemes": ["tar-app"]`, `"schemes": ["repo-app"]`},
		"id repeated":            {`"id": "tar-app"`, `"id": "repo-app"`},
		"id form":                {`"id": "tar-app"`, `"id": "Tar"`},
		"title missing":          {`"title": "Tar App",`, `"title": " ",`},
		"package entry":          {`"libsecret-1-0"`, `"libsecret 1"`},
	}
	for name, change := range broken {
		text := strings.Replace(desktopAppsManifest, change[0], change[1], 1)
		if text == desktopAppsManifest {
			t.Fatalf("%s: the change does not apply", name)
		}
		if _, err := parseManifest([]byte(text)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	// A link handler and an application cannot take the same scheme.
	withHandler := strings.Replace(desktopAppsManifest, `"contributes": {`, `"contributes": {
    "mcpServers": [{ "id": "node", "command": ["server.py"] }],
    "uriHandlers": [{ "scheme": "tar-app", "server": "node", "tool": "open" }],`, 1)
	if _, err := parseManifest([]byte(withHandler)); err == nil || !strings.Contains(err.Error(), "repeated") {
		t.Fatalf("shared scheme: %v", err)
	}
}
