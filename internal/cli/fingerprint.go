package cli

import (
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"math/big"
	"strings"
)

type hostKey struct {
	path, kind string
	validate   func(func() ([]byte, bool)) bool
}

var hostKeys = [...]hostKey{
	{"/etc/ssh/ssh_host_ed25519_key.pub", "ssh-ed25519", validEd25519Key},
	{"/etc/ssh/ssh_host_ecdsa_key.pub", "ecdsa-sha2-nistp256", validECDSAKey},
	{"/etc/ssh/ssh_host_rsa_key.pub", "ssh-rsa", validRSAKey},
}

// printFingerprints validates all supported SSH host public keys in a running sandbox before
// printing their SHA-256 fingerprints.
func printFingerprints(request Request, p podmanTarget, stdout io.Writer) error {
	objects, err := requireSandbox(request, p)
	if err != nil {
		return err
	}
	names, err := requireRunning(request, objects)
	if err != nil {
		return err
	}
	var output strings.Builder
	for _, key := range hostKeys {
		text, err := p.query("exec", "--user=1000:1000", "--env", "HOME=/home/agent", names.Container, "cat", key.path)
		if err != nil {
			return fmt.Errorf("fingerprint: read %s: %w", key.path, err)
		}
		blob, ok := hostPublicKey(text, key)
		if !ok {
			return fmt.Errorf("fingerprint: invalid public key in %s", key.path)
		}
		digest := sha256.Sum256(blob)
		fmt.Fprintf(&output, "%s SHA256:%s\n", key.kind, base64.RawStdEncoding.EncodeToString(digest[:]))
	}
	_, err = io.WriteString(stdout, output.String())
	return err
}

// hostPublicKey accepts one OpenSSH public key with the expected type. It checks
// the complete SSH wire structure and key material before any fingerprint is emitted.
func hostPublicKey(text string, key hostKey) ([]byte, bool) {
	text = strings.TrimSpace(text)
	fields := strings.Fields(text)
	if strings.ContainsAny(text, "\r\n") || len(fields) < 2 || fields[0] != key.kind {
		return nil, false
	}
	blob, err := base64.StdEncoding.Strict().DecodeString(fields[1])
	if err != nil {
		return nil, false
	}
	rest := blob
	readString := func() ([]byte, bool) {
		if len(rest) < 4 {
			return nil, false
		}
		length := uint64(binary.BigEndian.Uint32(rest[:4]))
		rest = rest[4:]
		if length > uint64(len(rest)) {
			return nil, false
		}
		value := rest[:int(length)]
		rest = rest[int(length):]
		return value, true
	}
	typeName, ok := readString()
	if !ok || string(typeName) != key.kind {
		return nil, false
	}
	return blob, key.validate(readString) && len(rest) == 0
}

// validEd25519Key checks that the SSH wire key contains a 32-byte Ed25519 public key.
func validEd25519Key(readString func() ([]byte, bool)) bool {
	key, ok := readString()
	return ok && len(key) == 32
}

// validECDSAKey checks that the SSH wire key names nistp256 and contains a point on that curve.
func validECDSAKey(readString func() ([]byte, bool)) bool {
	curve, ok := readString()
	if !ok || string(curve) != "nistp256" {
		return false
	}
	point, ok := readString()
	if !ok {
		return false
	}
	x, _ := elliptic.Unmarshal(elliptic.P256(), point)
	return x != nil
}

// validRSAKey checks that the SSH wire key contains positive RSA integers with a valid odd
// exponent and modulus.
func validRSAKey(readString func() ([]byte, bool)) bool {
	exponent, ok := readString()
	if !ok || !positiveSSHInteger(exponent) {
		return false
	}
	modulus, ok := readString()
	if !ok || !positiveSSHInteger(modulus) {
		return false
	}
	e, n := new(big.Int).SetBytes(exponent), new(big.Int).SetBytes(modulus)
	return e.IsInt64() && e.Int64() >= 3 && e.Int64() <= 1<<31-1 && e.Bit(0) != 0 && n.Bit(0) != 0 && n.Cmp(e) > 0
}

// positiveSSHInteger reports whether an SSH multiprecision integer is positive and uses its
// minimal signed encoding.
func positiveSSHInteger(value []byte) bool {
	if len(value) == 0 || value[0]&0x80 != 0 {
		return false
	}
	return value[0] != 0 || (len(value) > 1 && value[1]&0x80 != 0)
}
