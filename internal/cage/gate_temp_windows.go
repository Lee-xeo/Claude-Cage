package cage

import "github.com/sylvinus/agent-vm/internal/host"

// gateSocketRoom is what each run adds below the base: `\cage-gate-` and up
// to ten random digits, then `\control.sock` or a node's `\n-` and 24 hex.
const gateSocketRoom = len(`\cage-gate-`) + 10 + len(`\n-`) + 24

// Windows AF_UNIX sockets live in a private ASCII directory of the profile,
// outside AppData and Temp where they could not be used or removed (#484).
func gateSocketBase(profile string) (string, error) {
	return host.PrivateSocketDir(profile, gateSocketRoom, true)
}
