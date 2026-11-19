#!/usr/bin/python3
"""Open external links in the current Linux Desktop's own display and network."""
import os
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path
from urllib.parse import parse_qs, urlsplit


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
    regular = Path.home() / ".config/chromium"
    if not sign_in(url) or Path("/etc/claude-cage/login-browser-mode").exists():
        os.execv(browser, [browser, "--user-data-dir=" + str(regular), address])
    # Incognito windows in the same Chromium process can share cookies. A new
    # profile also gives each concurrent sign-in its own process. systemd
    # removes the runtime directory on stop even after an interrupted login.
    with tempfile.TemporaryDirectory(prefix="login-", dir=temporary) as profile:
        return subprocess.call([browser, "--user-data-dir=" + profile, "--incognito",
                                "--no-first-run", "--no-default-browser-check", address])


if __name__ == "__main__":
    try:
        sys.exit(main(sys.argv[1]) if len(sys.argv) == 2 else 2)
    except (OSError, ValueError):
        sys.exit(1)
