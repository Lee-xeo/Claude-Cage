// SPDX-License-Identifier: Apache-2.0

// Package guestidentity creates public, per-environment machine identifiers.
// These identifiers are not credentials and must never be used for authentication.
package guestidentity

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
)

type Identity struct {
	Hostname          string `json:"hostname" yaml:"hostname"`
	UUID              string `json:"uuid" yaml:"uuid"`
	MACAddress        string `json:"macAddress" yaml:"macAddress"`
	GatewayMACAddress string `json:"gatewayMacAddress" yaml:"gatewayMacAddress"`
}

// New uses independent random bytes for the hostname, UUID and both MACs.
// Persist its result at creation; reconnecting must reuse the saved identity.
func New() (*Identity, error) { return generate(rand.Reader) }

func generate(random io.Reader) (*Identity, error) {
	hostname, err := generateHostname(random)
	if err != nil {
		return nil, fmt.Errorf("generate guest hostname: %w", err)
	}
	var b [28]byte
	if _, err := io.ReadFull(random, b[:]); err != nil {
		return nil, err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	mac := func(raw []byte) string {
		raw[0] = raw[0]&0xfc | 0x02 // locally administered, unicast
		return net.HardwareAddr(raw).String()
	}
	guest, gateway := mac(b[16:22]), mac(b[22:28])
	if guest == gateway {
		b[27] ^= 1
		gateway = mac(b[22:28])
	}
	return &Identity{
		Hostname:   hostname,
		UUID:       fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:16]),
		MACAddress: guest, GatewayMACAddress: gateway,
	}, nil
}

var hostnamePattern = regexp.MustCompile(`^[A-Za-z](?:[A-Za-z0-9-]{0,13}[A-Za-z0-9])?$`)
var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func (i *Identity) Validate() error {
	if i == nil || !hostnamePattern.MatchString(i.Hostname) || !uuidPattern.MatchString(i.UUID) {
		return errors.New("invalid guest hostname or UUID")
	}
	for _, text := range []string{i.MACAddress, i.GatewayMACAddress} {
		mac, err := net.ParseMAC(text)
		if err != nil || len(mac) != 6 || mac.String() != text || mac[0]&3 != 2 {
			return errors.New("guest MAC must be a canonical locally administered unicast address")
		}
	}
	if i.MACAddress == i.GatewayMACAddress {
		return errors.New("guest and gateway MAC must differ")
	}
	return nil
}

// MachineID is the systemd/D-Bus representation of the environment UUID.
func (i *Identity) MachineID() string { return strings.ReplaceAll(i.UUID, "-", "") }
