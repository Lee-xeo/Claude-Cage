"""Publish only the latest native Desktop account observation to its runtime."""
import hashlib
import json
import os
from pathlib import Path
import re
import runpy
import sys
from uuid import UUID

color = re.compile(r"\x1b\[[0-9;]*m")
event = re.compile(r'^\[MAIN_LOG\] [\d-]+ [\d:]+ \[debug\] \[DATAPROVIDER\] (Setting|Resetting) "current-account"')
property_line = re.compile(r"""^  (isLoggedOut|accountUuid|organizationUuid|emailAddress): (true|false|'[^'\\\n]*'|"[^"\\\n]*"),?$""")


def collect(stream, publish):
    fields = None
    while raw := stream.readline(4096):
        if not raw.endswith(b"\n"):
            fields = None
            while raw and not raw.endswith(b"\n"):
                raw = stream.readline(4096)
            continue
        line = color.sub("", raw.decode("utf-8", errors="replace").rstrip("\r\n"))
        match = event.match(line)
        if match:
            fields = None
            payload = line[match.end():]
            if match[1] == "Resetting":
                publish(None)
            elif payload == " to: { isLoggedOut: true }":
                publish({"logged_in": False})
            elif payload == " to: {":
                fields = {}
            continue
        if fields is None:
            continue
        if line == "}":
            result = None
            if fields.get("isLoggedOut") == "true":
                result = {"logged_in": False}
            elif fields.get("isLoggedOut") == "false" and fields.get("emailAddress"):
                try:
                    account = str(UUID(fields.get("accountUuid", "")))
                    org = str(UUID(fields.get("organizationUuid", "")))
                    result = {"logged_in": True, "email": fields["emailAddress"], "organization": org, "account_uuid": account}
                except ValueError:
                    pass
            publish(result)
            fields = None
        elif not line.startswith("  "):
            fields = None
        elif item := property_line.fullmatch(line):
            fields[item[1]] = item[2].strip("'\"")


def main():
    os.umask(0o077)
    path = Path(sys.argv[1])
    temporary = path.with_suffix(".tmp")
    history = sys.argv[2:]
    previous = object()
    def publish(value):
        nonlocal previous
        if value != previous:
            public = {key: entry for key, entry in value.items() if key != "account_uuid"} if value else None
            temporary.write_text(json.dumps(public))
            temporary.replace(path)
            previous = value
            if history and value and value["logged_in"]:
                config, subject = history
                identity = value["email"].strip().lower() + "\n" + value["organization"]
                if hashlib.sha256(identity.encode()).hexdigest() == subject:
                    try:
                        recover = runpy.run_path(str(Path(__file__).with_name("desktop-history.py")))["recover"]
                        recover(Path.home(), Path(config), value["account_uuid"], value["organization"])
                    except OSError:
                        print("Could not restore Desktop history; will retry on the next launch.", file=sys.stderr)
    collect(sys.stdin.buffer, publish)


if __name__ == "__main__":
    main()
