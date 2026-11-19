package workbench

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

const delegatedFiles = `":root"="deny",":minimal"="read",":workspace_roots"={"."="write",".git"="read",".agents"="read",".codex"="read"}`

// BSD mktemp chooses the Darwin user temporary directory when the caller omits
// a template. Keep explicit templates and -p intact; only change that default.
const delegatedMktemp = `#!/bin/sh
while getopts 'dqut:p:' opt; do
    case "$opt" in p|\?) exec /usr/bin/mktemp "$@" ;; esac
done
if [ "$OPTIND" -gt "$#" ]; then
    exec /usr/bin/mktemp -p "$TMPDIR" "$@"
fi
exec /usr/bin/mktemp "$@"
`

func codexBuildConfig(ctx context.Context, dir, project string) ([]string, error) {
	base := "permissions.delegated.filesystem={" + delegatedFiles
	if runtime.GOOS != "darwin" {
		return []string{"-c", base + "}"}, nil
	}
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	scratch := filepath.Join(canonical, "build")
	bin := filepath.Join(canonical, "bin")
	for _, path := range []string{scratch, bin, filepath.Join(scratch, "tmp")} {
		if err := os.Mkdir(path, 0700); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "mktemp"), []byte(delegatedMktemp), 0700); err != nil {
		return nil, err
	}
	access := map[string]string{scratch: "write", bin: "read"}
	tmp := filepath.Join(scratch, "tmp")
	env := map[string]string{
		"PATH":                    bin + ":/usr/bin:/bin:/usr/sbin:/sbin",
		"TMPDIR":                  tmp,
		"GOTMPDIR":                tmp,
		"GOCACHE":                 filepath.Join(scratch, "go-build"),
		"GOPATH":                  filepath.Join(scratch, "gopath"),
		"GOFLAGS":                 "-modcacherw",
		"GOTOOLCHAIN":             "local",
		"GOENV":                   "off",
		"GOPROXY":                 "off",
		"GOTELEMETRY":             "off",
		"npm_config_cache":        filepath.Join(scratch, "npm"),
		"npm_config_userconfig":   "/dev/null",
		"XDG_CACHE_HOME":          filepath.Join(scratch, "cache"),
		"CLANG_MODULE_CACHE_PATH": filepath.Join(scratch, "modules"),
		"SWIFT_MODULECACHE_PATH":  filepath.Join(scratch, "swift-modules"),
	}
	for _, root := range []string{"/opt/homebrew/opt/go/libexec", "/usr/local/opt/go/libexec", "/usr/local/go"} {
		root, err = filepath.EvalSymlinks(root)
		if err != nil || !regularBuildFile(filepath.Join(root, "src", "runtime", "runtime2.go")) {
			continue
		}
		tool := filepath.Join(root, "bin", "go")
		if !regularBuildFile(tool) {
			continue
		}
		if err = os.Symlink(tool, filepath.Join(bin, "go")); err != nil {
			return nil, err
		}
		access[root] = "read"
		env["GOROOT"] = root
		break
	}
	nodes := []string{"/opt/homebrew/bin/node", "/usr/local/bin/node"}
	if home, err := os.UserHomeDir(); err == nil && filepath.IsAbs(home) {
		nodes = append([]string{filepath.Join(home, ".local", "bin", "node")}, nodes...)
		for _, candidate := range []string{filepath.Join(home, ".local", "bin", "uv"), "/opt/homebrew/bin/uv", "/usr/local/bin/uv"} {
			tool, err := filepath.EvalSymlinks(candidate)
			if err != nil || !regularBuildFile(tool) {
				continue
			}
			if err = os.Symlink(tool, filepath.Join(bin, "uv")); err != nil {
				return nil, err
			}
			access[tool] = "read"
			env["UV_CACHE_DIR"] = filepath.Join(scratch, "uv-cache")
			break
		}
	}
	for _, candidate := range nodes {
		node, err := filepath.EvalSymlinks(candidate)
		if err != nil || filepath.Base(node) != "node" || !regularBuildFile(node) {
			continue
		}
		if err = os.Symlink(node, filepath.Join(bin, "node")); err != nil {
			return nil, err
		}
		access[node] = "read"
		npm, err := filepath.EvalSymlinks(filepath.Join(filepath.Dir(filepath.Dir(node)), "lib", "node_modules", "npm"))
		if err == nil && filepath.Base(npm) == "npm" && regularBuildFile(filepath.Join(npm, "bin", "npm-cli.js")) {
			access[npm] = "read"
			for _, name := range []string{"npm", "npx"} {
				script := filepath.Join(npm, "bin", name+"-cli.js")
				if regularBuildFile(script) {
					if err = os.Symlink(script, filepath.Join(bin, name)); err != nil {
						return nil, err
					}
				}
			}
		}
		break
	}
	if ssl := "/System/Library/OpenSSL/openssl.cnf"; regularBuildFile(ssl) {
		access[ssl] = "read"
	}
	if data, err := exec.CommandContext(ctx, "/usr/bin/xcode-select", "-p").Output(); err == nil {
		developer, err := filepath.EvalSymlinks(strings.TrimSpace(string(data)))
		if err == nil && (developer == "/Library/Developer/CommandLineTools" ||
			(strings.HasPrefix(developer, "/Applications/") && strings.HasSuffix(developer, ".app/Contents/Developer"))) {
			env["DEVELOPER_DIR"] = developer
			access[developer] = "read"
		}
	}
	// A pre-provisioned file proxy contains only this project's locked
	// dependencies. Go expands and verifies modules in its private GOPATH.
	cache, err := os.UserCacheDir()
	if err == nil {
		key := sha256.Sum256([]byte(project))
		proxy := filepath.Join(cache, "Cage", "project-go-proxy", fmt.Sprintf("%x", key[:16]))
		if real, err := filepath.EvalSymlinks(proxy); err == nil && real == proxy {
			if info, err := os.Stat(proxy); err == nil && info.IsDir() {
				access[proxy] = "read"
				env["GOPROXY"] = (&url.URL{Scheme: "file", Path: proxy}).String()
			}
		}
	}
	// macOS login shells prepend system paths, which would select BSD mktemp
	// before the session-specific default. Keep the explicit environment.
	return []string{
		"-c", base + "," + buildTableEntries(access) + "}",
		"-c", "shell_environment_policy.set={" + buildTableEntries(env) + "}",
		"-c", "allow_login_shell=false",
		"-c", "features.shell_snapshot=false",
	}, nil
}

func regularBuildFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func buildTableEntries(values map[string]string) string {
	fields := make([]string, 0, len(values))
	for k, v := range values {
		fields = append(fields, strconv.Quote(k)+"="+strconv.Quote(v))
	}
	sort.Strings(fields)
	return strings.Join(fields, ",")
}
