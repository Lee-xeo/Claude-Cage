#!/usr/bin/python3
"""Open external links in the current Linux Desktop's own display and network."""
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path
from urllib.parse import parse_qs, urlsplit

LANG_RE = re.compile(r"^[A-Za-z]{2,3}(?:-[A-Za-z0-9]{2,8})*$")
POSIX_LOCALE_RE = re.compile(r"^([A-Za-z]{2,3})_([A-Za-z]{2,3})(?:\.[^@\s]+)?(?:@[^\s]+)?$")


def valid_language(value):
    value = (value or "").strip()
    return value if LANG_RE.match(value) else ""


def language_from_locale(value):
    match = POSIX_LOCALE_RE.match((value or "").strip())
    if not match:
        return ""
    return valid_language(f"{match.group(1).lower()}-{match.group(2).upper()}")


def read_env_file_language(path):
    try:
        for line in Path(path).read_text(encoding="utf-8", errors="replace").splitlines():
            key, sep, val = line.partition("=")
            if sep and key.strip() == "CAGE_LANGUAGE":
                lang = valid_language(val.strip().strip("'\""))
                if lang:
                    return lang
    except OSError:
        pass
    return ""


def resolve_language(env=None, runtime_dir="", env_file="/etc/claude-cage/environment"):
    env = os.environ if env is None else env
    runtime_dir = runtime_dir or env.get("XDG_RUNTIME_DIR", "")
    if runtime_dir:
        try:
            lang = valid_language(Path(runtime_dir, "browser-language").read_text(encoding="utf-8", errors="replace"))
            if lang:
                return lang
        except OSError:
            pass
    for candidate in (
        valid_language(env.get("CAGE_LANGUAGE", "")),
        read_env_file_language(env_file),
        language_from_locale(env.get("LC_ALL", "")),
        language_from_locale(env.get("LANG", "")),
    ):
        if candidate:
            return candidate
    return "en-US"


def save_language_prefs(profile_dir, lang):
    default_dir = Path(profile_dir) / "Default"
    default_dir.mkdir(parents=True, exist_ok=True)
    prefs_path = default_dir / "Preferences"
    data = {}
    try:
        parsed = json.loads(prefs_path.read_text(encoding="utf-8"))
        if isinstance(parsed, dict):
            data = parsed
    except (OSError, ValueError):
        pass
    intl = data.get("intl")
    if not isinstance(intl, dict):
        intl = {}
        data["intl"] = intl
    intl["accept_languages"] = lang
    intl["selected_languages"] = lang
    tmp_path = prefs_path.with_name("Preferences.tmp")
    tmp_path.write_text(json.dumps(data), encoding="utf-8")
    os.chmod(tmp_path, 0o600)
    os.replace(tmp_path, prefs_path)


def clean_environment(env=None):
    source = os.environ if env is None else env
    return {k: v for k, v in source.items() if not k.upper().startswith("CAGE_")}


def browser_flags(profile_dir, lang):
    return [
        "--user-data-dir=" + str(profile_dir),
        "--no-first-run",
        "--no-default-browser-check",
        "--disable-quic",
        "--force-webrtc-ip-handling-policy=disable_non_proxied_udp",
        "--disable-blink-features=AutomationControlled",
        "--lang=" + lang,
        "--accept-lang=" + lang,
    ]


def sign_in(url):
    if url.scheme != "https" or url.username or url.password or url.port not in (None, 443):
        return False
    path = url.path.removeprefix("/cai/")
    if not path.startswith("/"):
        path = "/" + path
    official = {"claude.ai", "claude.com", "platform.claude.com", "platform.anthropic.com", "console.anthropic.com"}
    if url.hostname in official:
        roots = ("/login", "/auth", "/oauth", "/sso", "/sso-callback", "/magic-link", "/desktop/authorize", "/desktop/callback", "/api/auth")
        return any(path == root or path.startswith(root + "/") for root in roots)
    provider = ((url.hostname == "api.workos.com" and path.startswith("/sso/")) or
                (url.hostname == "accounts.google.com" and path.startswith("/o/oauth2/")))
    redirects = parse_qs(url.query, keep_blank_values=True).get("redirect_uri", [])
    if not provider or len(redirects) != 1:
        return False
    try:
        callback = urlsplit(redirects[0])
        return (callback.scheme == "https" and not callback.username and not callback.password
                and callback.port in (None, 443) and callback.hostname in official)
    except ValueError:
        return False


def main(address):
    if len(address) > 8192 or any(ord(c) < 32 for c in address):
        return 2
    url = urlsplit(address)
    if url.scheme not in ("http", "https") or not url.hostname:
        return 2
    browser = shutil.which("chromium") or shutil.which("google-chrome-stable")
    if not browser:
        return 1
    temporary = os.environ["XDG_RUNTIME_DIR"]
    lang = resolve_language(os.environ, temporary)
    env = clean_environment(os.environ)
    regular = Path.home() / ".config/chromium"
    if not sign_in(url) or Path("/etc/claude-cage/login-browser-mode").exists():
        try:
            save_language_prefs(regular, lang)
        except OSError:
            pass
        os.execve(browser, [browser, *browser_flags(regular, lang), address], env)
    # Each sign-in gets a disposable profile directory in the private runtime
    # directory instead of --incognito, isolating cookies and concurrent flows
    # without setting incognito-detection signals. systemd removes the runtime
    # directory on stop even after an interrupted login.
    with tempfile.TemporaryDirectory(prefix="login-", dir=temporary) as profile:
        try:
            save_language_prefs(profile, lang)
        except OSError:
            pass
        return subprocess.call([browser, *browser_flags(profile, lang), address], env=env)


if __name__ == "__main__":
    try:
        sys.exit(main(sys.argv[1]) if len(sys.argv) == 2 else 2)
    except (OSError, ValueError):
        sys.exit(1)
