package workbench

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sylvinus/agent-vm/internal/hostbridge"
)

// guestPluginRoot is where plugin files are kept in the environment, under
// the user's home there. The name says nothing about this application.
const guestPluginRoot = ".local/share/plugins"

// guestPluginLimit is the most a plugin's files may add up to.
const guestPluginLimit = 100 << 20

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'" }

// packPlugin is the plugin's files as a tar archive and its digest. Only
// plain files and folders go in, as when a plugin is installed. The manifest
// stays behind: it is read here, and it names this application and where
// the plugin runs, which nothing in the environment needs to be told.
func packPlugin(dir string) ([]byte, string, error) {
	var buffer bytes.Buffer
	archive := tar.NewWriter(&buffer)
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || path == dir {
			return err
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if relative == hostbridge.ManifestFile {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		header := &tar.Header{Name: filepath.ToSlash(relative), Mode: 0o644}
		switch {
		case info.IsDir():
			header.Typeflag, header.Mode = tar.TypeDir, 0o755
			header.Name += "/"
			return archive.WriteHeader(header)
		case !info.Mode().IsRegular():
			return fmt.Errorf("%s is not a plain file", relative)
		}
		// Windows keeps no executable bit; there every file may be a program.
		if info.Mode().Perm()&0o111 != 0 || filepath.Separator == '\\' {
			header.Mode = 0o755
		}
		if int64(buffer.Len())+info.Size() > guestPluginLimit {
			return errors.New("the plugin is too large")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		header.Typeflag, header.Size = tar.TypeReg, int64(len(data))
		if err := archive.WriteHeader(header); err != nil {
			return err
		}
		_, err = archive.Write(data)
		return err
	})
	if err != nil {
		return nil, "", err
	}
	if err := archive.Close(); err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256(buffer.Bytes())
	return buffer.Bytes(), hex.EncodeToString(digest[:]), nil
}

// guestPluginSyncScript replaces the plugin's folder with the archive on its
// standard input, whole or not at all. $1 is the folder, $2 the digest.
const guestPluginSyncScript = `set -e
dir="$HOME/$1"
rm -rf "$dir.new"
mkdir -p "$dir.new"
tar -xf - -C "$dir.new"
printf '%s' "$2" > "$dir.new/.digest"
rm -rf "$dir"
mv "$dir.new" "$dir"`

// guestPluginRunScript runs the plugin's program in its folder. $1 is the
// folder, the rest the program and its arguments. The program is taken from
// the folder when it is there, as on this computer, and from PATH otherwise.
// One still running from before, as when it does not stop when its input
// closes, is ended first: it is known by the folder it runs in. The program
// gets the environment of an ordinary session, not the variables the
// environment's own services set for themselves.
const guestPluginRunScript = `dir="$HOME/$1"
shift
note="$dir.pid"
if [ -f "$note" ]; then
  old=$(cat "$note")
  case "$(readlink "/proc/$old/cwd" 2>/dev/null)" in "$dir"|"$dir (deleted)") kill "$old" 2>/dev/null ;; esac
fi
cd "$dir" || exit 1
echo $$ > "$note"
program=$1
shift
case "$program" in /*) ;; *) [ -f "./$program" ] && program="./$program" ;; esac
exec env -i HOME="$HOME" USER="$(id -un)" LOGNAME="$(id -un)" SHELL="${SHELL:-/bin/sh}" PATH="$PATH" LANG="${LANG:-C.UTF-8}" ${LC_ALL:+LC_ALL="$LC_ALL"} ${TZ:+TZ="$TZ"} "$program" "$@"`

// guestPluginCommand copies the plugin's files into the environment, unless
// they are there already, and returns the command that runs its server
// there, with its standard input and output carried over SSH.
func (s *Server) guestPluginCommand(ctx context.Context, manifest hostbridge.Manifest, dir string) (*exec.Cmd, error) {
	command := manifest.ServerCommand()
	if len(command) == 0 {
		return nil, errors.New("the plugin has no MCP server")
	}
	archive, digest, err := packPlugin(dir)
	if err != nil {
		return nil, err
	}
	folder := guestPluginRoot + "/" + manifest.Name
	prepare, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	current, _ := s.sshCommand(prepare, "-T", s.sshTarget(), "--", "cat "+shellQuote(folder+"/.digest")+" 2>/dev/null").Output()
	if strings.TrimSpace(string(current)) != digest {
		sync := s.sshCommand(prepare, "-T", s.sshTarget(), "--", "sh -c "+shellQuote(guestPluginSyncScript)+" sh "+shellQuote(folder)+" "+shellQuote(digest))
		sync.Stdin = bytes.NewReader(archive)
		if output, err := sync.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("copying the plugin: %w: %s", err, strings.TrimSpace(string(output)))
		}
	}
	line := "sh -c " + shellQuote(guestPluginRunScript) + " sh " + shellQuote(folder)
	for _, argument := range command {
		line += " " + shellQuote(argument)
	}
	// Not tied to ctx, which ends with the call that starts the program: the
	// plugin's own Close ends it.
	return s.sshCommand(s.lifetimeContext(), "-T", s.sshTarget(), "--", line), nil
}
