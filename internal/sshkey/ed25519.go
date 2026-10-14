// Package sshkey validates the ed25519 public keys exchanged with the manager.
package sshkey

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strings"
)

// Ed25519 returns the canonical key without its comment, accepting exactly one key.
func Ed25519(text string) (string, error) {
	text = strings.TrimSpace(text)
	fields := strings.Fields(text)
	if strings.ContainsAny(text, "\r\n") || len(fields) < 2 || fields[0] != "ssh-ed25519" {
		return "", fmt.Errorf("expected one ed25519 public key")
	}
	blob, err := base64.StdEncoding.Strict().DecodeString(fields[1])
	if err != nil || len(blob) != 51 || binary.BigEndian.Uint32(blob[:4]) != 11 || string(blob[4:15]) != "ssh-ed25519" || binary.BigEndian.Uint32(blob[15:19]) != 32 {
		return "", fmt.Errorf("expected one ed25519 public key")
	}
	return fields[0] + " " + fields[1], nil
}
