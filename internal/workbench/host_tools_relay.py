# Carries MCP messages between stdin/stdout and an HTTP MCP server, for
# clients that can only start a server as a local program.
# Usage: relay.py <endpoint.json>, a file holding {"url": ..., "headers": {...}}.
import json
import sys
import threading
import urllib.error
import urllib.request


class Relay:
    def __init__(self, endpoint, output):
        self.url = endpoint["url"]
        self.headers = dict(endpoint.get("headers") or {})
        self.output = output
        self.lock = threading.Lock()
        self.session = None
        self.version = None

    def write(self, message):
        with self.lock:
            self.output.write(json.dumps(message, separators=(",", ":")) + "\n")
            self.output.flush()

    def send(self, message):
        headers = dict(self.headers)
        headers["Content-Type"] = "application/json"
        headers["Accept"] = "application/json, text/event-stream"
        if self.session:
            headers["Mcp-Session-Id"] = self.session
        if self.version:
            headers["MCP-Protocol-Version"] = self.version
        request = urllib.request.Request(self.url, json.dumps(message).encode(), headers, method="POST")
        try:
            # A tool call may run for a long time; the server ends it.
            with urllib.request.urlopen(request, timeout=None) as response:
                self.session = response.headers.get("Mcp-Session-Id") or self.session
                kind = (response.headers.get("Content-Type") or "").split(";")[0].strip()
                if kind == "text/event-stream":
                    for reply in events(response):
                        self.deliver(reply)
                elif kind == "application/json":
                    self.deliver(json.loads(response.read()))
        except (urllib.error.URLError, OSError, ValueError) as error:
            self.fail(message, error)

    def deliver(self, reply):
        result = reply.get("result") if isinstance(reply, dict) else None
        if isinstance(result, dict) and "protocolVersion" in result:
            self.version = result["protocolVersion"]
        self.write(reply)

    def fail(self, message, error):
        # Only a request has an id to answer; a failed notification is dropped.
        if isinstance(message, dict) and "id" in message and "method" in message:
            reason = getattr(error, "reason", None) or error
            self.write({"jsonrpc": "2.0", "id": message["id"], "error": {"code": -32000, "message": "server unavailable: %s" % reason}})


def events(response):
    """Yields the JSON message of each server-sent event."""
    data = []
    for raw in response:
        line = raw.decode("utf-8").rstrip("\r\n")
        if line.startswith("data:"):
            data.append(line[5:].lstrip(" "))
        elif line == "" and data:
            text, data = "\n".join(data), []
            if text:
                yield json.loads(text)
    if data and "".join(data):
        yield json.loads("\n".join(data))


def serve(endpoint, lines, output):
    relay = Relay(endpoint, output)
    workers = []
    for line in lines:
        line = line.strip()
        if not line:
            continue
        try:
            message = json.loads(line)
        except ValueError:
            continue
        if isinstance(message, dict) and message.get("method") == "initialize":
            # Later messages need the session this one opens.
            relay.send(message)
            continue
        worker = threading.Thread(target=relay.send, args=(message,), daemon=True)
        worker.start()
        workers.append(worker)
    for worker in workers:
        worker.join()


if __name__ == "__main__":
    with open(sys.argv[1], encoding="utf-8") as source:
        serve(json.load(source), sys.stdin, sys.stdout)
