package managedproxy

import (
	"net/url"
	"slices"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestAccountNodeKeepsImportedDialerWhenEnvironmentOverridesIt(t *testing.T) {
	profile := t.TempDir()
	store, err := Load(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Import("private", "", []byte(testNodes), "source", 0, ""); err != nil {
		t.Fatal(err)
	}
	edge, exit := store.Sources[0].Nodes[0].ID, store.Sources[0].Nodes[1].ID
	store.Selected = exit
	store.Chain = []string{exit}
	data, err := store.Runtime(profile)
	if err != nil {
		t.Fatal(err)
	}
	var runtime struct {
		Authentication, Rules []string
		Proxies               []map[string]any
	}
	if err := yaml.Unmarshal(data, &runtime); err != nil {
		t.Fatal(err)
	}
	seenClone := false
	for _, p := range runtime.Proxies {
		if p["name"] == exit && p["dialer-proxy"] != nil {
			t.Fatal("environment chain was not applied")
		}
		if p["name"] == "route-"+exit && p["dialer-proxy"] == edge {
			seenClone = true
		}
	}
	if !seenClone {
		t.Fatal("account lost the imported chain")
	}
	for id, target := range map[string]string{edge: edge, exit: "route-" + exit} {
		endpoint, err := store.NodeEndpoint(id)
		if err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(endpoint)
		if err != nil {
			t.Fatal(err)
		}
		if endpoint == store.Endpoint() || !slices.Contains(runtime.Authentication, u.User.String()) || !slices.Contains(runtime.Rules, "IN-USER,"+u.User.Username()+","+target) {
			t.Fatal("wrong account credential or route")
		}
	}
	if runtime.Rules[len(runtime.Rules)-1] != "MATCH,REJECT" {
		t.Fatal("unmatched connection can escape")
	}
	if _, err := store.NodeEndpoint("missing"); err == nil {
		t.Fatal("missing node received credentials")
	}
}
