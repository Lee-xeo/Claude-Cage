"""Prepare a project-only offline Go proxy on the Mac that runs Codex."""
import argparse
import hashlib
import os
from pathlib import Path
import shutil
import subprocess
import tempfile


def git(project, *args):
    return subprocess.check_output(["git", "-C", project, *args], text=True)


def escape_module(value):
    # GOPROXY paths use Go's module.EscapePath/EscapeVersion uppercase encoding.
    return "".join("!" + c.lower() if "A" <= c <= "Z" else c for c in value)


def main():
    args = argparse.ArgumentParser(description=__doc__)
    args.add_argument("project", type=Path)
    args.add_argument("--sum", action="append", help="Committed go.sum to include (repeatable)")
    options = args.parse_args()
    project = options.project.resolve(strict=True)
    revision = git(project, "rev-parse", "--verify", "HEAD").strip()
    entries = {}
    for filename in options.sum or ["go.sum"]:
        path = Path(filename)
        if path.is_absolute() or ".." in path.parts or path.name != "go.sum":
            args.error("--sum must name a go.sum inside the repository")
        for line in git(project, "show", f"{revision}:{filename}").splitlines():
            module, version, digest = line.split()
            mod_only = version.endswith("/go.mod")
            version = version.removesuffix("/go.mod")
            if (Path(module).is_absolute() or ".." in Path(module).parts or
                    not version.startswith("v") or "/" in version or not digest.startswith("h1:")):
                raise ValueError("Invalid module checksum entry")
            entries.setdefault((module, version), {})["mod" if mod_only else "zip"] = digest

    cache = Path(subprocess.check_output(["go", "env", "GOMODCACHE"], text=True).strip()) / "cache/download"
    project_key = hashlib.sha256(os.fsencode(project)).hexdigest()[:32]
    dest = Path.home() / "Library/Caches/Cage/project-go-proxy" / project_key
    dest.mkdir(mode=0o700, parents=True, exist_ok=True)
    if dest.resolve() != dest:
        raise ValueError("The dedicated proxy directory cannot be a symbolic link")
    copied = 0
    for (module, version), checksums in entries.items():
        prefix = Path(escape_module(module)) / "@v" / escape_module(version)
        extensions = [".mod", ".info"]
        if "zip" in checksums:
            check = cache / (str(prefix) + ".ziphash")
            if check.is_file() and check.read_text().strip() == checksums["zip"]:
                extensions.append(".zip")
        for ext in extensions:
            relative = Path(str(prefix) + ext)
            src, out = cache / relative, dest / relative
            if out.is_file() or not src.is_file() or src.resolve() != src:
                continue
            out.parent.mkdir(parents=True, exist_ok=True)
            with tempfile.NamedTemporaryFile(dir=out.parent, delete=False) as f:
                tmp = Path(f.name)
            try:
                shutil.copyfile(src, tmp)
                os.replace(tmp, out)
                copied += 1
            finally:
                tmp.unlink(missing_ok=True)
    print(f"Prepared {copied} additional cached module artifacts for {project.name}")


if __name__ == "__main__":
    main()
