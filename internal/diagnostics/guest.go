package diagnostics

import (
	"encoding/json"
	"strings"
)

// GuestRunner uses the same redaction expressions as the host, before journald
// receives service output. stdin and systemd notify sockets remain inherited.
func GuestRunner() string {
	rules := make([][2]string, 0, len(patterns))
	for _, p := range patterns {
		rules = append(rules, [2]string{p.re.String(), strings.ReplaceAll(p.replacement, "${1}", `\g<1>`)})
	}
	b, _ := json.Marshal(rules)
	return "#!/usr/bin/python3\n" + `import json, os, re, signal, subprocess, sys
rules = [(re.compile(pattern), replacement) for pattern, replacement in json.loads(` + string(mustJSON(string(b))) + `)]
host_in_message = re.compile(` + string(mustJSON(hostInMessage.String())) + `)
public_diagnostic = re.compile(` + string(mustJSON(publicDiagnostic.String())) + `)
private_block_begin = re.compile(` + string(mustJSON(privateBlockBegin.String())) + `)
def redact_host(match):
    return match[0] if public_diagnostic.fullmatch(match[0]) else match[1] + '[host]'
# Handle stop signals before the child exists, so a stop that arrives while
# it is starting is still forwarded and reported as requested.
child = None
stopped_signal = 0
def forward(sig):
    try:
        child.send_signal(sig)
    except ProcessLookupError:
        pass
def stop(sig, _frame):
    global stopped_signal
    stopped_signal = sig
    if child is not None:
        forward(sig)
for sig in (signal.SIGTERM, signal.SIGINT, signal.SIGHUP):
    signal.signal(sig, stop)
child = subprocess.Popen(sys.argv[1:], stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
if stopped_signal:
    forward(stopped_signal)
private_key = False
while True:
    data = child.stdout.readline(24577)
    if not data:
        break
    if len(data) > 24576:
        while data and not data.endswith(b'\n'):
            data = child.stdout.readline(24577)
        print('[oversized output line omitted]', flush=True)
        continue
    line = data.decode('utf-8', 'replace')
    if private_block_begin.search(line):
        private_key = True
    if private_key:
        if '-----END ' in line:
            private_key = False
        continue
    for pattern, replacement in rules:
        line = pattern.sub(lambda match: match.expand(replacement), line)
    print(host_in_message.sub(redact_host, line).rstrip(), flush=True)
code = child.wait()
if stopped_signal and code in (-stopped_signal, 128 + stopped_signal):
    code = 0
sys.exit(code if code >= 0 else 128-code)
`
}
func mustJSON(v string) []byte { b, _ := json.Marshal(v); return b }
