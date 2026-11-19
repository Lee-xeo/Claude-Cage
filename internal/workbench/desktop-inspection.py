"""Read only public identity notifications from the local Desktop process."""
import json
import re
import subprocess
from pathlib import Path

identifier = re.compile(r"[0-9a-f]{24}")
home = Path.home()
base = home / ".local/share/desktop-profiles"
units = subprocess.check_output([
    "systemctl", "show", "--property=Id,ActiveState,MainPID", "desktop-*.service",
    "cage-claude-desktop*.service"
], text=True)
running = {}
for unit in units.strip().split("\n\n"):
    values = dict(line.split("=", 1) for line in unit.splitlines() if "=" in line)
    if values.get("ActiveState") == "active":
        running[values["Id"]] = int(values["MainPID"])
result = {}
def inspect(instance, runtime, unit):
    active = unit in running
    result[instance] = {"running": active, "generation": running.get(unit, 0)}
    if active:
        try:
            with Path("/run", runtime, "account.json").open() as status:
                observation = json.loads(status.read(4096))
        except (FileNotFoundError, ValueError):
            return
        if isinstance(observation, dict):
            result[instance]["identity"] = observation

if (home / ".config/Claude").is_dir() or "cage-claude-desktop.service" in running:
    inspect("default", "cage-desktop", "cage-claude-desktop.service")
if base.is_dir():
    for folder in base.iterdir():
        if identifier.fullmatch(folder.name) and (folder / "launch.lock").is_file():
            unit = "desktop-" + folder.name
            inspect(folder.name, unit, unit + ".service")
print(json.dumps(result, separators=(",", ":")))
