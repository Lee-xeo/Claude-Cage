# A complete plugin: an MCP server on standard input and output with one
# tool. It needs nothing beyond Python's standard library.
import datetime
import json
import sys

TOOLS = [{
    "name": "current_time",
    "description": "Return the current local date and time on the user's computer.",
    "inputSchema": {"type": "object", "properties": {}},
}]


def answer(message):
    method = message.get("method")
    if method == "initialize":
        return {
            "protocolVersion": message["params"]["protocolVersion"],
            "capabilities": {"tools": {}},
            "serverInfo": {"name": "clock", "version": "0.1.0"},
        }
    if method == "tools/list":
        return {"tools": TOOLS}
    if method == "tools/call" and message["params"]["name"] == "current_time":
        now = datetime.datetime.now().astimezone().isoformat(timespec="seconds")
        return {"content": [{"type": "text", "text": now}]}
    if method == "ping":
        return {}
    return None


for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    request = json.loads(line)
    if "id" not in request:
        continue  # a notification needs no reply
    result = answer(request)
    reply = {"jsonrpc": "2.0", "id": request["id"]}
    if result is None:
        reply["error"] = {"code": -32601, "message": "method not found"}
    else:
        reply["result"] = result
    sys.stdout.write(json.dumps(reply) + "\n")
    sys.stdout.flush()
