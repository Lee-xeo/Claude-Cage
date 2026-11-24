#!/usr/bin/env python3
"""MCP server for authoring, validating, and submitting custom extensions."""

from __future__ import annotations

import datetime
import hashlib
import json
import os
import re
import shutil
import stat
import subprocess
import sys
import tempfile
from pathlib import Path
from typing import Any

NAME_RE = re.compile(r"^[a-z0-9]+(-[a-z0-9]+)*$")
VERSION_RE = re.compile(r"^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$")
ENV_RE = re.compile(r"^[A-Z][A-Z0-9_]{0,63}$")
OPEN_URL_RE = re.compile(r"^(https?)://([a-z0-9.-]+)(?::(\*|\d{1,5}))?(/[^\s#]*)$", re.IGNORECASE)

KNOWN_PERMISSIONS = ("files", "commands", "screen", "input", "network")
KNOWN_CATEGORIES = ("desktop", "developer", "system", "network", "files", "productivity", "example")
KNOWN_PLATFORMS = ("darwin", "linux", "windows")
KNOWN_TARGETS = ("workspace", "local")
RESERVED_ENV = {"HOME", "USER", "LOGNAME", "SHELL", "PATH", "LANG", "LC_ALL", "TZ"}
RESERVED_TOOL_NAMES = {"run_command", "command_job"}
ALLOWED_VIEW_EXTS = {
    ".html", ".htm", ".js", ".mjs", ".css", ".json", ".txt",
    ".svg", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico",
    ".woff", ".woff2", ".ttf", ".otf", ".wasm",
}

MAX_FILES = 2000
MAX_TOTAL_BYTES = 100 * 1024 * 1024
MAX_VIEW_FILE_BYTES = 20 * 1024 * 1024
MAX_ICON_BYTES = 256 * 1024
IGNORED_DIR_NAMES = {"__pycache__", ".git", ".pytest_cache"}


def drafts_root() -> Path:
    custom = os.environ.get("PLUGIN_DRAFTS_DIR", "").strip()
    if custom:
        return Path(custom)
    return Path.home() / ".local" / "share" / "plugin-drafts"


def submissions_root() -> Path:
    custom = os.environ.get("PLUGIN_SUBMISSIONS_DIR", "").strip()
    if custom:
        return Path(custom)
    return Path.home() / ".local" / "share" / "plugin-submissions"


def market_catalog_path() -> Path:
    custom = os.environ.get("PLUGIN_MARKET_CATALOG", "").strip()
    if custom:
        return Path(custom)
    return Path(__file__).resolve().parent / "market-catalog.json"


GUIDE_SECTIONS: dict[str, str] = {
    "overview": """# Custom Plugin Authoring Guide

A plugin is a folder containing `extension.json`, an optional stdio MCP server, and an optional sandboxed sidebar HTML view.

## Workflow
0. First call `search_plugins` to check whether a ready-made plugin already exists in the plugin market or is already installed. If a good match exists, recommend installing or using it instead of rebuilding from scratch.
1. If no existing plugin fits the need, call `plugin_scaffold` to create a working draft under the drafts directory.
2. Edit files inside the draft directory (write `extension.json`, your MCP server, and optional `view/` files).
3. Call `plugin_validate` to run manifest checks, file/CSP checks, and a live MCP `initialize` + `tools/list` smoke test.
4. Fix any reported errors and call `plugin_submit_draft`. The user will review the files and permissions in the workbench and click Install & Reload.

## Neutral Manifest (`extension.json`)
Required fields:
- `name`: lowercase letters, digits, hyphens (`^[a-z0-9]+(-[a-z0-9]+)*$`, max 40 chars, must match folder name).
- `version`: semantic version like `"0.1.0"`.
- `description`: concise one-sentence functional summary.
- `target`: `"workspace"` (the default; runs beside the current session without a confirmation per call) or `"local"` (for tools that need the user's desktop apps, screen or input; the user confirms each call).
- `permissions`: array of required capabilities from `["files", "commands", "screen", "input", "network"]`, or `[]`.
- `contributes`: object containing `mcpServers` (at most 1 server), optional `views`, and optional `settings`.

Optional fields: `displayName` (max 80 chars), `icon` (`.svg` or `.png` <= 256 KB), `categories` (up to 3 from `desktop`, `developer`, `system`, `network`, `files`, `productivity`, `example`), `platforms` (only when `target` is `"local"`: `darwin`, `linux`, `windows`).
No unknown fields are allowed anywhere in `extension.json`.
""",
    "mcp": """# MCP Server Rules

- Communicate via newline-delimited JSON-RPC 2.0 over stdin/stdout. Never print logs to stdout (use stderr).
- Implement `initialize`, `tools/list`, `tools/call`, and `ping`. Notifications (messages without `id`) must not produce a reply.
- Tool names and descriptions must describe ONLY what the tool does (avoid internal/environment jargon). Do not reuse reserved tool names `run_command` or `command_job`.
- Tool visibility (`_meta.ui.visibility`):
  - Omit or `["model", "app"]`: available to both the assistant and the extension's sidebar view.
  - `["app"]`: only callable by the extension's sidebar view (for user-clicked actions).
  - `["model"]`: only callable by the assistant.
- Parameter length limit: keep tool call arguments concise (local calls reject arguments > 4000 bytes or containing invisible control characters; write large content to files and pass file paths instead).
- Command arguments may use `${PROJECT_DIR}` to receive the currently active project folder path.
""",
    "view": """# Sandboxed Sidebar View Rules

Views run inside a `sandbox="allow-scripts"` iframe without `allow-same-origin`.

## Strict CSP Restrictions
- All scripts must be in separate `.js` / `.mjs` files inside the entry HTML's directory (NO inline `<script>...</script>`, NO inline `onclick="..."` handlers).
- Inline `<style>` and `data:` images are allowed; external network requests (`fetch`, `XMLHttpRequest`), `eval`, `new Function`, `Worker`, `localStorage`, `alert`, and `window.open` are NOT available.
- Only static files with allowed extensions (`.html`, `.js`, `.mjs`, `.css`, `.json`, `.txt`, `.svg`, `.png`, `.jpg`, `.jpeg`, `.gif`, `.webp`, `.ico`, `.woff`, `.woff2`, `.ttf`, `.otf`, `.wasm`) up to 20 MiB each are served.

## JSON-RPC 2.0 Bridge (`window.parent.postMessage`)
Call `ui/initialize` (`{"protocolVersion": 1}`) first. It returns `{protocolVersion, plugin, view, presentation, theme: {scheme, tokens}, locale, state}`.
Set `"presentation": "floating"` (with optional `"width"` 220..720 and `"height"` 100..720) in `contributes.views[]` to open the view as a draggable translucent floating overlay instead of a full page.
Available methods:
- `tools/list`, `tools/call` (`{name, arguments}`)
- `ui/setState` (`{state}`, max 64 KiB) to persist view state across reopen
- `ui/resize` (`{width?, height?}`) to adjust a floating view's dimensions
- `ui/notify` (`{message, tone?: "info"|"error"}`)
- `ui/confirm` (`{title, message, confirmLabel?, danger?}`)
- `ui/copy` (`{text}`, max 64 KiB)
- `ui/openUrl` (`{url}`, must match `views[].openUrls`)
Notifications from host: `ui/themeChanged` (`{scheme, tokens}`), `ui/visibilityChanged` (`{visible}`).
Theme token names: `paper`, `ink`, `strong`, `muted`, `line`, `chrome`, `hover`, `selected`, `accent`, `link`, `focus`, `input`, `button`, `button-ink`, `success`, `danger`.
""",
    "settings": """# User Settings & Secrets (`contributes.settings`)

Declare up to 20 user-configurable settings. Values are passed to your MCP server as environment variables (never on argv):
```json
"settings": [
  {"id": "endpoint", "title": "Service URL", "type": "string", "env": "MY_EXT_URL", "required": true},
  {"id": "api-key", "title": "API Token", "type": "secret", "env": "MY_EXT_TOKEN", "required": true}
]
```
- `type`: `"string"`, `"secret"` (encrypted at rest, cannot have `default`), `"boolean"` (`"true"`/`"false"`), or `"choice"` (requires 1..20 unique `choices`).
- `env`: `^[A-Z][A-Z0-9_]{0,63}$`, unique per extension, cannot be `HOME`, `USER`, `LOGNAME`, `SHELL`, `PATH`, `LANG`, `LC_ALL`, `TZ`, or start with `LD_` / `DYLD_`.
""",
}

TOOLS: list[dict[str, Any]] = [
    {
        "name": "search_plugins",
        "description": "Search available plugins in the plugin market and currently installed plugins by keyword or category. Call this first when the user asks for a new capability; if no existing plugin matches, build one with plugin_scaffold.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "query": {
                    "type": "string",
                    "description": "Search keywords matching plugin name, title, description, or category (e.g. 'excel', 'blender', 'pdf', 'browser'). Omit to list available plugins.",
                },
                "category": {
                    "type": "string",
                    "enum": ["desktop", "developer", "system", "network", "files", "productivity", "example"],
                    "description": "Optional category filter.",
                },
                "installed_only": {
                    "type": "boolean",
                    "description": "When true, return only plugins that are already installed.",
                },
                "limit": {
                    "type": "integer",
                    "minimum": 1,
                    "maximum": 30,
                    "description": "Maximum number of matches to return (default 10).",
                },
            },
        },
        "_meta": {"ui": {"visibility": ["model"]}},
    },
    {
        "name": "plugin_get_guide",
        "description": "Read the concise specification for authoring custom MCP tools, sandboxed sidebar views, and settings.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "topic": {
                    "type": "string",
                    "enum": ["overview", "mcp", "view", "settings"],
                    "description": "Optional section to read. Omit for the full guide.",
                }
            },
        },
        "_meta": {"ui": {"visibility": ["model"]}},
    },
    {
        "name": "plugin_scaffold",
        "description": "Create a new custom plugin draft folder with starter manifest, MCP server, and optional sandboxed view.",
        "inputSchema": {
            "type": "object",
            "required": ["name"],
            "properties": {
                "name": {
                    "type": "string",
                    "description": "Plugin identifier: lowercase letters, digits, and hyphens (max 40 chars).",
                },
                "template": {
                    "type": "string",
                    "enum": ["mcp-python", "mcp-node", "mcp-with-view-python"],
                    "description": "Starter template to generate. Defaults to mcp-python.",
                },
                "display_name": {
                    "type": "string",
                    "description": "Optional human-friendly display name (max 80 chars).",
                },
                "description": {
                    "type": "string",
                    "description": "Optional one-sentence functional description.",
                },
                "target": {
                    "type": "string",
                    "enum": ["workspace", "local"],
                    "description": "Execution target: workspace (default) or local.",
                },
                "overwrite": {
                    "type": "boolean",
                    "description": "Set true to replace an existing draft folder with the same name.",
                },
            },
        },
        "_meta": {"ui": {"visibility": ["model"]}},
    },
    {
        "name": "plugin_validate",
        "description": "Validate a plugin draft: checks manifest schema, file safety, view CSP rules, and runs a live MCP smoke test.",
        "inputSchema": {
            "type": "object",
            "required": ["name"],
            "properties": {
                "name": {
                    "type": "string",
                    "description": "Draft folder name under the plugin drafts directory.",
                }
            },
        },
        "_meta": {"ui": {"visibility": ["model"]}},
    },
    {
        "name": "plugin_submit_draft",
        "description": "Validate and submit a plugin draft snapshot so the user can review its code and permissions and install it.",
        "inputSchema": {
            "type": "object",
            "required": ["name"],
            "properties": {
                "name": {
                    "type": "string",
                    "description": "Draft folder name to validate and submit for user review.",
                },
                "summary": {
                    "type": "string",
                    "description": "Short summary of what this plugin does or what changed, shown during review.",
                },
            },
        },
        "_meta": {"ui": {"visibility": ["model"]}},
    },
    {
        "name": "plugin_list_drafts",
        "description": "List existing custom plugin drafts and whether each has been submitted for review.",
        "inputSchema": {
            "type": "object",
            "properties": {},
        },
        "_meta": {"ui": {"visibility": ["model"]}},
    },
]


def valid_id(value: Any) -> bool:
    return isinstance(value, str) and 1 <= len(value) <= 40 and bool(NAME_RE.match(value))


def valid_rel_path(value: Any, exts: tuple[str, ...] | None = None) -> bool:
    if not isinstance(value, str) or not value or len(value) > 200:
        return False
    if "\\" in value or value.startswith("/") or value.endswith("/"):
        return False
    parts = value.split("/")
    if any(p in ("", ".", "..") for p in parts):
        return False
    if exts is not None:
        lower = value.lower()
        if not any(lower.endswith(ext) for ext in exts):
            return False
    return True


def valid_open_url(pattern: Any) -> bool:
    if not isinstance(pattern, str) or not pattern or len(pattern) > 200:
        return False
    match = OPEN_URL_RE.match(pattern)
    if not match:
        return False
    scheme, host, port, path_part = match.groups()
    scheme = scheme.lower()
    host = host.lower()
    if "*" in host:
        return False
    if scheme == "http":
        if host not in ("127.0.0.1", "localhost"):
            return False
    elif scheme == "https":
        if host in ("localhost", "127.0.0.1"):
            return False
    else:
        return False
    if port and port != "*":
        try:
            port_num = int(port)
        except ValueError:
            return False
        if not (1 <= port_num <= 65535):
            return False
    if "*" in path_part[:-1]:
        return False
    return True


def is_probably_text(data: bytes) -> bool:
    if b"\x00" in data:
        return False
    try:
        data.decode("utf-8")
        return True
    except UnicodeDecodeError:
        return False


def python_server_template(name: str, tool_name: str, description: str) -> str:
    return f'''#!/usr/bin/env python3
import datetime
import json
import sys

TOOLS = [
    {{
        "name": "{tool_name}",
        "description": {json.dumps(description)},
        "inputSchema": {{
            "type": "object",
            "properties": {{
                "message": {{
                    "type": "string",
                    "description": "Optional input text.",
                }}
            }},
        }},
    }}
]


def handle_call(tool_name, arguments):
    if tool_name == "{tool_name}":
        message = (arguments or {{}}).get("message", "ready")
        now = datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="seconds")
        return {{
            "content": [
                {{"type": "text", "text": f"[{{now}}] {{message}}"}}
            ]
        }}
    return {{
        "isError": True,
        "content": [{{"type": "text", "text": f"unknown tool: {{tool_name}}"}}],
    }}


def answer(message):
    method = message.get("method")
    params = message.get("params") or {{}}
    if method == "initialize":
        return {{
            "protocolVersion": params.get("protocolVersion", "2024-11-05"),
            "capabilities": {{"tools": {{}}}},
            "serverInfo": {{"name": {json.dumps(name)}, "version": "0.1.0"}},
        }}
    if method == "tools/list":
        return {{"tools": TOOLS}}
    if method == "tools/call":
        return handle_call(params.get("name", ""), params.get("arguments") or {{}})
    if method == "ping":
        return {{}}
    return None


def main():
    for raw in sys.stdin:
        line = raw.strip()
        if not line:
            continue
        request = json.loads(line)
        if "id" not in request:
            continue
        result = answer(request)
        reply = {{"jsonrpc": "2.0", "id": request["id"]}}
        if result is None:
            reply["error"] = {{"code": -32601, "message": "method not found"}}
        else:
            reply["result"] = result
        sys.stdout.write(json.dumps(reply) + "\\n")
        sys.stdout.flush()


if __name__ == "__main__":
    main()
'''


def node_server_template(name: str, tool_name: str, description: str) -> str:
    return f'''#!/usr/bin/env node
const readline = require("node:readline");

const TOOLS = [
  {{
    name: {json.dumps(tool_name)},
    description: {json.dumps(description)},
    inputSchema: {{
      type: "object",
      properties: {{
        message: {{ type: "string", description: "Optional input text." }}
      }}
    }}
  }}
];

function answer(msg) {{
  const params = msg.params || {{}};
  if (msg.method === "initialize") {{
    return {{
      protocolVersion: params.protocolVersion || "2024-11-05",
      capabilities: {{ tools: {{}} }},
      serverInfo: {{ name: {json.dumps(name)}, version: "0.1.0" }}
    }};
  }}
  if (msg.method === "tools/list") return {{ tools: TOOLS }};
  if (msg.method === "tools/call") {{
    if (params.name === {json.dumps(tool_name)}) {{
      const text = (params.arguments && params.arguments.message) || "ready";
      return {{ content: [{{ type: "text", text: `[${{new Date().toISOString()}}] ${{text}}` }}] }};
    }}
    return {{ isError: true, content: [{{ type: "text", text: `unknown tool: ${{params.name}}` }}] }};
  }}
  if (msg.method === "ping") return {{}};
  return null;
}}

const rl = readline.createInterface({{ input: process.stdin, crlfDelay: Infinity }});
rl.on("line", (line) => {{
  const trimmed = line.trim();
  if (!trimmed) return;
  const req = JSON.parse(trimmed);
  if (!Object.prototype.hasOwnProperty.call(req, "id")) return;
  const result = answer(req);
  const reply = result === null
    ? {{ jsonrpc: "2.0", id: req.id, error: {{ code: -32601, message: "method not found" }} }}
    : {{ jsonrpc: "2.0", id: req.id, result }};
  process.stdout.write(JSON.stringify(reply) + "\\n");
}});
'''


def view_html_template(title: str) -> str:
    return f'''<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>{title}</title>
  <link rel="stylesheet" href="style.css" />
</head>
<body>
  <main class="panel">
    <header class="header">
      <h1>{title}</h1>
      <button id="refresh" type="button">Refresh</button>
    </header>
    <pre id="output" class="output">Loading...</pre>
  </main>
  <script type="module" src="app.js"></script>
</body>
</html>
'''


def view_css_template() -> str:
    return '''* { box-sizing: border-box; }
body {
  margin: 0;
  padding: 20px;
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
  background: var(--paper, #ffffff);
  color: var(--ink, #1f2937);
}
.panel {
  max-width: 760px;
  margin: 0 auto;
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
h1 {
  margin: 0;
  font-size: 18px;
  color: var(--strong, #111827);
}
button {
  padding: 6px 12px;
  border-radius: 8px;
  border: 1px solid var(--line, #d1d5db);
  background: var(--button, #2563eb);
  color: var(--button-ink, #ffffff);
  cursor: pointer;
}
.output {
  margin: 0;
  padding: 12px;
  border-radius: 8px;
  border: 1px solid var(--line, #e5e7eb);
  background: var(--chrome, #f9fafb);
  white-space: pre-wrap;
  word-break: break-word;
}
'''


def view_js_template(tool_name: str) -> str:
    return f'''const pending = new Map();
let nextId = 1;

function request(method, params) {{
  return new Promise((resolve, reject) => {{
    const id = nextId++;
    pending.set(id, {{ resolve, reject }});
    window.parent.postMessage({{ jsonrpc: "2.0", id, method, params }}, "*");
  }});
}}

function applyTheme(theme) {{
  if (!theme || !theme.tokens) return;
  for (const [key, value] of Object.entries(theme.tokens)) {{
    document.documentElement.style.setProperty(`--${{key}}`, value);
  }}
}}

window.addEventListener("message", (event) => {{
  const message = event.data;
  if (event.source !== window.parent || !message || message.jsonrpc !== "2.0") return;
  if (message.method === "ui/themeChanged") {{
    applyTheme(message.params);
    return;
  }}
  const entry = pending.get(message.id);
  if (!entry) return;
  pending.delete(message.id);
  if (message.error) entry.reject(new Error(message.error.message || "request failed"));
  else entry.resolve(message.result);
}});

const output = document.getElementById("output");
const refreshButton = document.getElementById("refresh");

async function load() {{
  output.textContent = "Running...";
  try {{
    const result = await request("tools/call", {{ name: {json.dumps(tool_name)}, arguments: {{ message: "view refresh" }} }});
    const text = (result.content || []).map((item) => item.text || "").join("\\n");
    output.textContent = text || JSON.stringify(result, null, 2);
  }} catch (err) {{
    output.textContent = `Error: ${{err.message}}`;
  }}
}}

refreshButton.addEventListener("click", load);

const init = await request("ui/initialize", {{ protocolVersion: 1 }});
applyTheme(init.theme);
await load();
'''


def default_icon_svg() -> str:
    return '''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" fill="none">
  <rect width="64" height="64" rx="14" fill="#1E293B"/>
  <rect x="16" y="16" width="32" height="32" rx="8" stroke="#38BDF8" stroke-width="3"/>
  <circle cx="32" cy="32" r="6" fill="#38BDF8"/>
</svg>
'''


def tool_scaffold(args: dict[str, Any]) -> dict[str, Any]:
    name = args.get("name", "")
    if not valid_id(name):
        return err_result("name must be 1-40 lowercase letters, digits, or hyphens (e.g. my-tool)")
    template = args.get("template") or "mcp-python"
    if template not in ("mcp-python", "mcp-node", "mcp-with-view-python"):
        return err_result("template must be one of mcp-python, mcp-node, mcp-with-view-python")
    target = args.get("target") or "workspace"
    if target not in KNOWN_TARGETS:
        return err_result("target must be workspace or local")
    display_name = (args.get("display_name") or " ".join(p.capitalize() for p in name.split("-")))[:80]
    description = (args.get("description") or f"Custom extension for {display_name}.").strip()
    overwrite = bool(args.get("overwrite"))

    root = drafts_root()
    root.mkdir(parents=True, exist_ok=True)
    draft_dir = root / name
    if draft_dir.exists():
        if not overwrite:
            return err_result(f"draft '{name}' already exists at {draft_dir}; pass overwrite=true to replace it")
        shutil.rmtree(draft_dir)

    draft_dir.mkdir(parents=True, exist_ok=True)
    tool_name = name.replace("-", "_") + "_run"
    server_cmd = ["node", "server.js"] if template == "mcp-node" else ["python3", "server.py"]
    contributes: dict[str, Any] = {
        "mcpServers": [{"id": "main", "command": server_cmd}],
    }
    if template == "mcp-with-view-python":
        contributes["views"] = [
            {
                "id": "panel",
                "title": display_name,
                "icon": "icon.svg",
                "entry": "view/index.html",
                "server": "main",
            }
        ]
    manifest = {
        "name": name,
        "displayName": display_name,
        "version": "0.1.0",
        "description": description,
        "icon": "icon.svg",
        "target": target,
        "permissions": [],
        "categories": ["developer"],
        "contributes": contributes,
    }
    (draft_dir / "extension.json").write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    (draft_dir / "icon.svg").write_text(default_icon_svg(), encoding="utf-8")

    created_files = ["extension.json", "icon.svg"]
    if template == "mcp-node":
        server_path = draft_dir / "server.js"
        server_path.write_text(node_server_template(name, tool_name, description), encoding="utf-8")
        server_path.chmod(0o755)
        created_files.append("server.js")
    else:
        server_path = draft_dir / "server.py"
        server_path.write_text(python_server_template(name, tool_name, description), encoding="utf-8")
        server_path.chmod(0o755)
        created_files.append("server.py")

    if template == "mcp-with-view-python":
        view_dir = draft_dir / "view"
        view_dir.mkdir(parents=True, exist_ok=True)
        (view_dir / "index.html").write_text(view_html_template(display_name), encoding="utf-8")
        (view_dir / "style.css").write_text(view_css_template(), encoding="utf-8")
        (view_dir / "app.js").write_text(view_js_template(tool_name), encoding="utf-8")
        created_files.extend(["view/index.html", "view/style.css", "view/app.js"])

    return ok_json({
        "name": name,
        "draft_dir": str(draft_dir),
        "template": template,
        "files": created_files,
        "next_step": "Edit the files in draft_dir, then run plugin_validate and plugin_submit_draft.",
    })


def validate_manifest_dict(data: Any, expected_name: str) -> tuple[list[str], list[str], dict[str, Any] | None]:
    errors: list[str] = []
    warnings: list[str] = []
    if not isinstance(data, dict):
        return ["extension.json must be a JSON object"], warnings, None

    allowed_top = {
        "name", "displayName", "version", "description", "icon",
        "target", "platforms", "permissions", "categories", "contributes",
    }
    unknown = sorted(set(data.keys()) - allowed_top)
    if unknown:
        errors.append(f"extension.json contains unknown top-level fields: {', '.join(unknown)}")

    name = data.get("name")
    if not valid_id(name):
        errors.append("name must be 1-40 lowercase letters, digits, or hyphens")
    elif name != expected_name:
        errors.append(f"manifest name '{name}' must match draft folder name '{expected_name}'")

    if "displayName" in data:
        dn = data["displayName"]
        if not isinstance(dn, str) or not dn.strip() or len(dn) > 80:
            errors.append("displayName must be a non-empty string up to 80 characters")

    version = data.get("version")
    if not isinstance(version, str) or not VERSION_RE.match(version):
        errors.append("version must be semantic version X.Y.Z (e.g. 0.1.0)")

    desc = data.get("description")
    if not isinstance(desc, str) or not desc.strip() or len(desc) > 600:
        errors.append("description must be a non-empty string up to 600 characters")

    if "icon" in data and data["icon"] != "":
        if not valid_rel_path(data["icon"], (".svg", ".png")):
            errors.append("icon must be a relative .svg or .png path inside the extension folder")

    target = data.get("target")
    if target not in KNOWN_TARGETS:
        errors.append("target must be 'workspace' or 'local'")

    if "platforms" in data:
        if target == "workspace":
            errors.append("platforms is only allowed when target is 'local'")
        elif not isinstance(data["platforms"], list) or not all(p in KNOWN_PLATFORMS for p in data["platforms"]):
            errors.append("platforms must be a list of darwin, linux, or windows")

    perms = data.get("permissions")
    if not isinstance(perms, list) or not all(isinstance(p, str) and p in KNOWN_PERMISSIONS for p in perms):
        errors.append(f"permissions must be a list using only: {', '.join(KNOWN_PERMISSIONS)}")
    elif len(set(perms)) != len(perms):
        errors.append("permissions must not contain duplicates")

    if "categories" in data:
        cats = data["categories"]
        if not isinstance(cats, list) or len(cats) > 3 or not all(isinstance(c, str) and c in KNOWN_CATEGORIES for c in cats):
            errors.append(f"categories may list at most 3 values from: {', '.join(KNOWN_CATEGORIES)}")

    contributes = data.get("contributes")
    if not isinstance(contributes, dict):
        errors.append("contributes must be an object")
        return errors, warnings, None

    allowed_contributes = {"mcpServers", "views", "settings"}
    unknown_c = sorted(set(contributes.keys()) - allowed_contributes)
    if unknown_c:
        errors.append(f"contributes contains unknown fields: {', '.join(unknown_c)}")

    servers = contributes.get("mcpServers") or []
    views = contributes.get("views") or []
    settings = contributes.get("settings") or []
    if not isinstance(servers, list) or not isinstance(views, list) or not isinstance(settings, list):
        errors.append("contributes.mcpServers, views, and settings must be arrays when present")
        return errors, warnings, None

    if len(servers) == 0 and len(views) == 0:
        errors.append("contributes must define at least one mcpServer or one view")
    if len(servers) > 1:
        errors.append("at most one entry in contributes.mcpServers is currently supported")

    server_ids: set[str] = set()
    for idx, srv in enumerate(servers):
        if not isinstance(srv, dict):
            errors.append(f"mcpServers[{idx}] must be an object")
            continue
        unknown_s = sorted(set(srv.keys()) - {"id", "command"})
        if unknown_s:
            errors.append(f"mcpServers[{idx}] has unknown fields: {', '.join(unknown_s)}")
        sid = srv.get("id")
        if not valid_id(sid):
            errors.append(f"mcpServers[{idx}].id is invalid")
        elif sid in server_ids:
            errors.append(f"duplicate mcpServers id '{sid}'")
        else:
            server_ids.add(sid)
        cmd = srv.get("command")
        if not isinstance(cmd, list) or not cmd or not all(isinstance(arg, str) and arg != "" for arg in cmd):
            errors.append(f"mcpServers[{idx}].command must be a non-empty array of strings")
        elif "${PROJECT_DIR}" in cmd[0]:
            errors.append(f"mcpServers[{idx}].command[0] cannot contain ${{PROJECT_DIR}}")

    view_ids: set[str] = set()
    for idx, view in enumerate(views):
        if not isinstance(view, dict):
            errors.append(f"views[{idx}] must be an object")
            continue
        unknown_v = sorted(set(view.keys()) - {"id", "title", "icon", "entry", "server", "openUrls", "presentation", "width", "height"})
        if unknown_v:
            errors.append(f"views[{idx}] has unknown fields: {', '.join(unknown_v)}")
        vid = view.get("id")
        if not valid_id(vid):
            errors.append(f"views[{idx}].id is invalid")
        elif vid in view_ids:
            errors.append(f"duplicate views id '{vid}'")
        else:
            view_ids.add(vid)
        vtitle = view.get("title")
        if not isinstance(vtitle, str) or not vtitle.strip() or len(vtitle) > 80:
            errors.append(f"views[{idx}].title must be a non-empty string up to 80 chars")
        ventry = view.get("entry")
        if not valid_rel_path(ventry, (".html", ".htm")):
            errors.append(f"views[{idx}].entry must be a relative .html/.htm file path")
        if "icon" in view and view["icon"] != "":
            if not valid_rel_path(view["icon"], (".svg", ".png")):
                errors.append(f"views[{idx}].icon must be a relative .svg or .png file path")
        if "server" in view and view["server"] != "":
            if view["server"] not in server_ids:
                errors.append(f"views[{idx}].server '{view['server']}' does not match any mcpServers id")
        if "openUrls" in view:
            open_urls = view["openUrls"]
            if not isinstance(open_urls, list) or not all(valid_open_url(u) for u in open_urls):
                errors.append(f"views[{idx}].openUrls contains an invalid URL pattern")
        vpresentation = view.get("presentation", "")
        if vpresentation != "" and vpresentation not in ("page", "floating"):
            errors.append(f"views[{idx}].presentation must be 'page' or 'floating'")
        for dim, lo, hi in (("width", 220, 720), ("height", 100, 720)):
            if dim in view:
                if vpresentation != "floating":
                    errors.append(f"views[{idx}].{dim} is only allowed when presentation is 'floating'")
                elif not isinstance(view[dim], int) or isinstance(view[dim], bool) or not (lo <= view[dim] <= hi):
                    errors.append(f"views[{idx}].{dim} must be an integer between {lo} and {hi}")

    if len(settings) > 20:
        errors.append("contributes.settings supports at most 20 items")
    setting_ids: set[str] = set()
    setting_envs: set[str] = set()
    for idx, st in enumerate(settings):
        if not isinstance(st, dict):
            errors.append(f"settings[{idx}] must be an object")
            continue
        unknown_st = sorted(set(st.keys()) - {"id", "title", "description", "type", "env", "required", "default", "choices"})
        if unknown_st:
            errors.append(f"settings[{idx}] has unknown fields: {', '.join(unknown_st)}")
        stid = st.get("id")
        if not valid_id(stid) or stid in setting_ids:
            errors.append(f"settings[{idx}].id is invalid or duplicated")
        else:
            setting_ids.add(stid)
        sttitle = st.get("title")
        if not isinstance(sttitle, str) or not sttitle.strip() or len(sttitle) > 80:
            errors.append(f"settings[{idx}].title must be 1-80 characters")
        if "description" in st and (not isinstance(st["description"], str) or len(st["description"]) > 500):
            errors.append(f"settings[{idx}].description must be at most 500 characters")
        sttype = st.get("type")
        if sttype not in ("string", "secret", "boolean", "choice"):
            errors.append(f"settings[{idx}].type must be string, secret, boolean, or choice")
        env_name = st.get("env")
        if (
            not isinstance(env_name, str)
            or not ENV_RE.match(env_name)
            or env_name in RESERVED_ENV
            or env_name.startswith("LD_")
            or env_name.startswith("DYLD_")
            or env_name in setting_envs
        ):
            errors.append(f"settings[{idx}].env '{env_name}' is invalid, reserved, or duplicated")
        else:
            setting_envs.add(env_name)
        default_val = st.get("default", "")
        if default_val != "":
            if not isinstance(default_val, str) or len(default_val) > 8192 or any(c in default_val for c in ("\x00", "\r", "\n")):
                errors.append(f"settings[{idx}].default is invalid")
            if sttype == "secret":
                errors.append(f"settings[{idx}] of type secret cannot define a default value")
            if sttype == "boolean" and default_val not in ("true", "false"):
                errors.append(f"settings[{idx}] of type boolean default must be 'true' or 'false'")
        if sttype == "choice":
            choices = st.get("choices")
            if not isinstance(choices, list) or not (1 <= len(choices) <= 20) or len(set(choices)) != len(choices):
                errors.append(f"settings[{idx}] of type choice requires 1-20 unique choices")
            elif default_val and default_val not in choices:
                errors.append(f"settings[{idx}].default must be one of choices")
        elif "choices" in st and st["choices"]:
            errors.append(f"settings[{idx}].choices is only valid when type is choice")

    return errors, warnings, data


def scan_draft_files(draft_dir: Path) -> tuple[list[dict[str, Any]], list[str]]:
    errors: list[str] = []
    files: list[dict[str, Any]] = []
    total_bytes = 0
    if not draft_dir.exists() or not draft_dir.is_dir() or draft_dir.is_symlink():
        return files, ["draft directory does not exist or is not a plain folder"]

    for dirpath, dirnames, filenames in os.walk(draft_dir, followlinks=False):
        dirnames[:] = sorted(d for d in dirnames if d not in IGNORED_DIR_NAMES)
        for dname in list(dirnames):
            full_d = Path(dirpath) / dname
            if full_d.is_symlink():
                rel = full_d.relative_to(draft_dir).as_posix()
                errors.append(f"symbolic links are not allowed: {rel}")
                dirnames.remove(dname)
        for fname in sorted(filenames):
            full_f = Path(dirpath) / fname
            rel = full_f.relative_to(draft_dir).as_posix()
            st = os.lstat(full_f)
            if not stat.S_ISREG(st.st_mode):
                errors.append(f"only plain files are allowed: {rel}")
                continue
            total_bytes += st.st_size
            if len(files) >= MAX_FILES or total_bytes > MAX_TOTAL_BYTES:
                errors.append("extension exceeds file count (2000) or size limit (100 MiB)")
                return files, errors
            raw = full_f.read_bytes()
            files.append({
                "path": rel,
                "size": len(raw),
                "sha256": hashlib.sha256(raw).hexdigest(),
                "text": is_probably_text(raw),
            })
    return files, errors


def check_view_csp(draft_dir: Path, view: dict[str, Any]) -> tuple[list[str], list[str]]:
    errors: list[str] = []
    warnings: list[str] = []
    entry_rel = view.get("entry", "")
    entry_path = draft_dir / Path(entry_rel)
    if not entry_path.is_file():
        return [f"view entry file not found: {entry_rel}"], warnings

    view_dir = entry_path.parent
    for root_dir, _, filenames in os.walk(view_dir, followlinks=False):
        for fname in sorted(filenames):
            full = Path(root_dir) / fname
            rel = full.relative_to(draft_dir).as_posix()
            ext = full.suffix.lower()
            if ext not in ALLOWED_VIEW_EXTS:
                errors.append(f"view folder contains disallowed file extension: {rel}")
            if full.stat().st_size > MAX_VIEW_FILE_BYTES:
                errors.append(f"view file exceeds 20 MiB limit: {rel}")

    html = entry_path.read_text(encoding="utf-8", errors="replace")
    for match in re.finditer(r"<script\b([^>]*)>(.*?)</script>", html, re.IGNORECASE | re.DOTALL):
        attrs, body = match.group(1), match.group(2)
        if not re.search(r'\bsrc\s*=', attrs, re.IGNORECASE) and body.strip():
            errors.append(f"inline <script> is blocked by sandbox CSP in {entry_rel}; move JS to a separate file")
    if re.search(r'\bon[a-z]+\s*=\s*["\']', html, re.IGNORECASE):
        errors.append(f"inline event handlers (on*=) are blocked by sandbox CSP in {entry_rel}")

    found_init = False
    for root_dir, _, filenames in os.walk(view_dir, followlinks=False):
        for fname in sorted(filenames):
            if not fname.lower().endswith((".js", ".mjs")):
                continue
            full = Path(root_dir) / fname
            rel = full.relative_to(draft_dir).as_posix()
            code = full.read_text(encoding="utf-8", errors="replace")
            if "ui/initialize" in code:
                found_init = True
            for pattern, label in (
                (r"\beval\s*\(", "eval()"),
                (r"\bnew\s+Function\s*\(", "new Function()"),
                (r"\blocalStorage\b", "localStorage (use ui/setState instead)"),
                (r"\bwindow\.open\s*\(", "window.open() (use ui/openUrl instead)"),
                (r"\bfetch\s*\(", "fetch() (sandboxed views cannot access network; call MCP tools instead)"),
                (r"\bXMLHttpRequest\b", "XMLHttpRequest (sandboxed views cannot access network)"),
                (r"\bnew\s+Worker\s*\(", "Worker"),
            ):
                if re.search(pattern, code):
                    errors.append(f"disallowed sandbox API in {rel}: {label}")
    if not found_init:
        warnings.append(f"no script in {view_dir.relative_to(draft_dir).as_posix() or '.'} mentions 'ui/initialize'; views must call ui/initialize first")
    return errors, warnings


def smoke_test_mcp(draft_dir: Path, command: list[str], target: str) -> tuple[list[str], list[str], list[dict[str, Any]]]:
    errors: list[str] = []
    warnings: list[str] = []
    discovered_tools: list[dict[str, Any]] = []

    program = command[0]
    args = command[1:]
    if any("${PROJECT_DIR}" in a for a in args):
        args = [a.replace("${PROJECT_DIR}", str(draft_dir)) for a in args]

    resolved = program
    if "/" not in program and (draft_dir / program).is_file():
        resolved = str(draft_dir / program)
    elif "/" in program and not os.path.isabs(program):
        resolved = str(draft_dir / program)
    elif shutil.which(program) is None:
        if target == "local":
            warnings.append(f"program '{program}' is not installed in this workspace; smoke test deferred until installation")
            return errors, warnings, discovered_tools
        return [f"MCP command program not found: {program}"], warnings, discovered_tools

    clean_env = {
        key: os.environ[key]
        for key in ("HOME", "USER", "LOGNAME", "SHELL", "PATH", "LANG", "LC_ALL", "TZ")
        if key in os.environ
    }
    proc = None
    try:
        proc = subprocess.Popen(
            [resolved, *args],
            cwd=str(draft_dir),
            env=clean_env,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
        )
        assert proc.stdin is not None and proc.stdout is not None
        messages = [
            {
                "jsonrpc": "2.0",
                "id": 1,
                "method": "initialize",
                "params": {
                    "protocolVersion": "2024-11-05",
                    "capabilities": {},
                    "clientInfo": {"name": "validator", "version": "1"},
                },
            },
            {"jsonrpc": "2.0", "method": "notifications/initialized"},
            {"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}},
        ]
        payload = "\n".join(json.dumps(m) for m in messages) + "\n"
        stdout, stderr = proc.communicate(payload, timeout=10)
    except subprocess.TimeoutExpired:
        if proc is not None:
            proc.kill()
            proc.communicate()
        return ["MCP server timed out during initialize/tools/list (10s limit)"], warnings, discovered_tools
    except Exception as exc:
        return [f"failed to start MCP server: {exc}"], warnings, discovered_tools

    replies: dict[Any, dict[str, Any]] = {}
    for line in stdout.splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            obj = json.loads(line)
        except json.JSONDecodeError:
            errors.append(f"MCP server wrote non-JSON output to stdout: {line[:120]}")
            continue
        if isinstance(obj, dict) and "id" in obj:
            replies[obj["id"]] = obj

    init_reply = replies.get(1)
    if not init_reply or "error" in init_reply or "result" not in init_reply:
        detail = stderr.strip()[:300]
        errors.append(f"MCP server did not return a valid initialize response{': ' + detail if detail else ''}")
        return errors, warnings, discovered_tools

    list_reply = replies.get(2)
    if not list_reply or "error" in list_reply or not isinstance(list_reply.get("result"), dict):
        errors.append("MCP server did not return a valid tools/list response")
        return errors, warnings, discovered_tools

    raw_tools = list_reply["result"].get("tools")
    if not isinstance(raw_tools, list):
        errors.append("tools/list result.tools must be an array")
        return errors, warnings, discovered_tools

    seen_names: set[str] = set()
    for tool in raw_tools:
        if not isinstance(tool, dict) or not isinstance(tool.get("name"), str) or not tool["name"].strip():
            errors.append("each tool must have a non-empty string name")
            continue
        tname = tool["name"]
        if tname in seen_names:
            errors.append(f"duplicate tool name '{tname}'")
        seen_names.add(tname)
        ui_meta = (tool.get("_meta") or {}).get("ui") if isinstance(tool.get("_meta"), dict) else None
        vis = ui_meta.get("visibility") if isinstance(ui_meta, dict) else None
        page_only = isinstance(vis, list) and "model" not in vis and "app" in vis
        if not page_only and tname in RESERVED_TOOL_NAMES:
            errors.append(f"tool name '{tname}' is reserved; choose a distinct name")
        discovered_tools.append({
            "name": tname,
            "description": tool.get("description", ""),
            "page_only": page_only,
        })

    return errors, warnings, discovered_tools


def run_validation(name: str) -> dict[str, Any]:
    if not valid_id(name):
        return {"ok": False, "errors": ["invalid draft name"], "warnings": [], "tools": [], "files": []}
    draft_dir = drafts_root() / name
    files, file_errors = scan_draft_files(draft_dir)
    if file_errors:
        return {"ok": False, "errors": file_errors, "warnings": [], "tools": [], "files": files}

    manifest_path = draft_dir / "extension.json"
    if not manifest_path.is_file():
        return {"ok": False, "errors": ["missing extension.json in draft directory"], "warnings": [], "tools": [], "files": files}

    try:
        manifest_raw = json.loads(manifest_path.read_text(encoding="utf-8"))
    except Exception as exc:
        return {"ok": False, "errors": [f"extension.json is not valid JSON: {exc}"], "warnings": [], "tools": [], "files": files}

    errors, warnings, manifest = validate_manifest_dict(manifest_raw, name)
    discovered_tools: list[dict[str, Any]] = []
    if manifest:
        icon = manifest.get("icon")
        if icon:
            icon_path = draft_dir / Path(icon)
            if not icon_path.is_file():
                warnings.append(f"icon file '{icon}' was not found")
            elif icon_path.stat().st_size > MAX_ICON_BYTES:
                errors.append(f"icon file '{icon}' exceeds 256 KB")

        contributes = manifest.get("contributes") or {}
        for view in contributes.get("views") or []:
            v_err, v_warn = check_view_csp(draft_dir, view)
            errors.extend(v_err)
            warnings.extend(v_warn)

        servers = contributes.get("mcpServers") or []
        if servers and not errors:
            m_err, m_warn, discovered_tools = smoke_test_mcp(draft_dir, servers[0]["command"], manifest.get("target", "workspace"))
            errors.extend(m_err)
            warnings.extend(m_warn)

    return {
        "ok": len(errors) == 0,
        "name": name,
        "draft_dir": str(draft_dir),
        "manifest": manifest,
        "errors": errors,
        "warnings": warnings,
        "tools": discovered_tools,
        "files": files,
    }


def tool_submit_draft(args: dict[str, Any]) -> dict[str, Any]:
    name = args.get("name", "")
    summary = str(args.get("summary") or "").strip()[:500]
    report = run_validation(name)
    if not report["ok"]:
        return err_result("validation failed; fix errors before submitting:\n" + json.dumps(report, indent=2))

    draft_dir = drafts_root() / name
    sub_root = submissions_root()
    sub_root.mkdir(parents=True, exist_ok=True)
    target_dir = sub_root / name

    staging = Path(tempfile.mkdtemp(prefix=f".submitting-{name}-", dir=str(sub_root)))
    try:
        files_out = staging / "files"
        files_out.mkdir(parents=True, exist_ok=True)
        for item in report["files"]:
            rel = item["path"]
            src = draft_dir / Path(rel)
            dst = files_out / Path(rel)
            dst.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(src, dst)

        manifest = report["manifest"] or {}
        submission = {
            "id": name,
            "name": name,
            "displayName": manifest.get("displayName", ""),
            "version": manifest.get("version", ""),
            "description": manifest.get("description", ""),
            "summary": summary,
            "target": manifest.get("target", "workspace"),
            "platforms": manifest.get("platforms", []),
            "permissions": manifest.get("permissions", []),
            "categories": manifest.get("categories", []),
            "contributes": manifest.get("contributes", {}),
            "submittedAt": datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="seconds"),
            "tools": report["tools"],
            "warnings": report["warnings"],
            "files": report["files"],
        }
        (staging / "submission.json").write_text(json.dumps(submission, indent=2) + "\n", encoding="utf-8")

        backup = None
        if target_dir.exists():
            backup = Path(tempfile.mkdtemp(prefix=f".replacing-{name}-", dir=str(sub_root)))
            shutil.rmtree(backup)
            target_dir.rename(backup)
        try:
            staging.rename(target_dir)
        except Exception:
            if backup and backup.exists():
                backup.rename(target_dir)
            raise
        if backup and backup.exists():
            shutil.rmtree(backup, ignore_errors=True)
    except Exception as exc:
        shutil.rmtree(staging, ignore_errors=True)
        return err_result(f"failed to freeze submission snapshot: {exc}")

    return ok_json({
        "submitted": True,
        "id": name,
        "version": submission["version"],
        "target": submission["target"],
        "tools": submission["tools"],
        "warnings": submission["warnings"],
        "message": "Draft validated and submitted. Ask the user to open the Plugins panel -> Pending Drafts to review and install.",
    })


def tool_list_drafts() -> dict[str, Any]:
    d_root = drafts_root()
    s_root = submissions_root()
    items = []
    if d_root.is_dir():
        for entry in sorted(d_root.iterdir(), key=lambda p: p.name):
            if not entry.is_dir() or entry.name.startswith(".") or not valid_id(entry.name):
                continue
            sub_meta = s_root / entry.name / "submission.json"
            submitted_at = ""
            if sub_meta.is_file():
                try:
                    submitted_at = json.loads(sub_meta.read_text(encoding="utf-8")).get("submittedAt", "")
                except Exception:
                    submitted_at = ""
            manifest_info: dict[str, Any] = {}
            mpath = entry / "extension.json"
            if mpath.is_file():
                try:
                    manifest_info = json.loads(mpath.read_text(encoding="utf-8"))
                except Exception:
                    manifest_info = {}
            items.append({
                "name": entry.name,
                "path": str(entry),
                "display_name": manifest_info.get("displayName", ""),
                "version": manifest_info.get("version", ""),
                "target": manifest_info.get("target", ""),
                "submitted": bool(submitted_at),
                "submitted_at": submitted_at,
            })
    return ok_json({"drafts_dir": str(d_root), "drafts": items})


def tool_search_plugins(args: dict[str, Any]) -> dict[str, Any]:
    query = str(args.get("query") or "").strip().lower()
    category = str(args.get("category") or "").strip().lower()
    installed_only = bool(args.get("installed_only"))
    raw_limit = args.get("limit", 10)
    try:
        limit = max(1, min(30, int(raw_limit)))
    except (TypeError, ValueError):
        limit = 10

    catalog_file = market_catalog_path()
    catalog_data: dict[str, Any] = {"updated_at": "", "plugins": []}
    if catalog_file.is_file():
        try:
            loaded = json.loads(catalog_file.read_text(encoding="utf-8"))
            if isinstance(loaded, dict) and isinstance(loaded.get("plugins"), list):
                catalog_data = loaded
        except Exception:
            pass

    tokens = [tok for tok in re.split(r"[\s,;/|_-]+", query) if tok]
    scored: list[tuple[int, dict[str, Any]]] = []
    for item in catalog_data.get("plugins") or []:
        if not isinstance(item, dict):
            continue
        if installed_only and not item.get("installed"):
            continue
        cats = [str(c).lower() for c in (item.get("categories") or [])]
        if category and category not in cats:
            continue

        name = str(item.get("name") or "").lower()
        display = str(item.get("display_name") or "").lower()
        desc = str(item.get("description") or "").lower()
        localized = " ".join(
            f"{v.get('displayName', '')} {v.get('description', '')}"
            for v in (item.get("i18n") or {}).values()
            if isinstance(v, dict)
        ).lower()
        haystack = f"{name} {display} {desc} {' '.join(cats)} {localized}"

        if tokens:
            score = 0
            for tok in tokens:
                if tok == name:
                    score += 100
                elif tok in name:
                    score += 40
                elif tok in display or tok in cats:
                    score += 25
                elif tok in desc or tok in localized:
                    score += 12
            if score == 0:
                continue
        else:
            score = 10 if item.get("installed") else 1

        if item.get("installed"):
            score += 5
        scored.append((score, item))

    scored.sort(key=lambda pair: (-pair[0], str(pair[1].get("name") or "")))
    matches = []
    for _, item in scored[:limit]:
        matches.append({
            "name": item.get("name", ""),
            "display_name": item.get("display_name", ""),
            "version": item.get("version", ""),
            "description": item.get("description", ""),
            "categories": item.get("categories", []),
            "permissions": item.get("permissions", []),
            "target": item.get("target", "workspace"),
            "installed": bool(item.get("installed")),
            "installed_version": item.get("installed_version", ""),
            "supported": bool(item.get("supported", True)),
            "has_view": bool(item.get("has_view")),
            "has_mcp": bool(item.get("has_mcp", True)),
        })

    if any(m["installed"] for m in matches):
        recommendation = "Matching plugin(s) are already installed. Use their tools directly, or ask the user to enable them in Plugins if turned off."
    elif matches:
        recommendation = "Ready-made matching plugin(s) exist in the plugin market. Recommend that the user install one from the Plugins Market instead of building a duplicate."
    elif not catalog_data.get("plugins"):
        recommendation = "The local plugin market index is currently empty or still loading. You may ask the user to refresh the Plugins Market, or create a custom plugin using plugin_get_guide and plugin_scaffold."
    else:
        recommendation = "No existing plugin matches this request. Call plugin_get_guide and plugin_scaffold to create a custom plugin, validate it with plugin_validate, and submit it with plugin_submit_draft."

    return ok_json({
        "query": query,
        "category": category,
        "catalog_updated_at": catalog_data.get("updated_at", ""),
        "total_catalog_count": len(catalog_data.get("plugins") or []),
        "matches": matches,
        "recommendation": recommendation,
    })


def ok_json(payload: Any) -> dict[str, Any]:
    return {"content": [{"type": "text", "text": json.dumps(payload, indent=2, ensure_ascii=False)}]}


def err_result(message: str) -> dict[str, Any]:
    return {"isError": True, "content": [{"type": "text", "text": message}]}


def handle_tool_call(name: str, args: dict[str, Any]) -> dict[str, Any]:
    if name == "search_plugins":
        return tool_search_plugins(args or {})
    if name == "plugin_get_guide":
        topic = (args or {}).get("topic")
        if topic:
            if topic not in GUIDE_SECTIONS:
                return err_result("unknown topic; choose from overview, mcp, view, settings")
            return {"content": [{"type": "text", "text": GUIDE_SECTIONS[topic]}]}
        full = "\n\n".join(GUIDE_SECTIONS[k] for k in ("overview", "mcp", "view", "settings"))
        return {"content": [{"type": "text", "text": full}]}
    if name == "plugin_scaffold":
        return tool_scaffold(args or {})
    if name == "plugin_validate":
        return ok_json(run_validation((args or {}).get("name", "")))
    if name == "plugin_submit_draft":
        return tool_submit_draft(args or {})
    if name == "plugin_list_drafts":
        return tool_list_drafts()
    return err_result(f"unknown tool: {name}")


def answer(message: dict[str, Any]) -> dict[str, Any] | None:
    method = message.get("method")
    params = message.get("params") or {}
    if method == "initialize":
        return {
            "protocolVersion": params.get("protocolVersion", "2024-11-05"),
            "capabilities": {"tools": {}},
            "serverInfo": {"name": "plugin-studio", "version": "0.1.0"},
        }
    if method == "tools/list":
        return {"tools": TOOLS}
    if method == "tools/call":
        return handle_tool_call(params.get("name", ""), params.get("arguments") or {})
    if method == "ping":
        return {}
    return None


def main() -> None:
    for raw in sys.stdin:
        line = raw.strip()
        if not line:
            continue
        request = json.loads(line)
        if "id" not in request:
            continue
        result = answer(request)
        reply: dict[str, Any] = {"jsonrpc": "2.0", "id": request["id"]}
        if result is None:
            reply["error"] = {"code": -32601, "message": "method not found"}
        else:
            reply["result"] = result
        sys.stdout.write(json.dumps(reply) + "\n")
        sys.stdout.flush()


if __name__ == "__main__":
    main()
