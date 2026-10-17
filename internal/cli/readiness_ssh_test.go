package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestSSHReadinessWithRealClient proves only a correctly signed exchange reaches readiness using the host's real ssh against a test-only transport peer, as required by ADR-0008.
func TestSSHReadinessWithRealClient(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("host ssh is unavailable")
	}
	for _, signature := range []string{"valid", "corrupt", "absent"} {
		t.Run(signature, func(t *testing.T) {
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			done := make(chan error, 1)
			go func() {
				c, err := listener.Accept()
				if err == nil {
					defer c.Close()
					_ = c.SetDeadline(time.Now().Add(5 * time.Second))
					err = serveTestExchange(c, signature)
				}
				done <- err
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var log string
			err = probeSSH(ctx, uint16(listener.Addr().(*net.TCPAddr).Port), func(_ context.Context, cmd *exec.Cmd) ([]byte, error) {
				out, err := cmd.Output()
				log = string(out)
				return out, err
			})
			listener.Close()
			serverErr := <-done
			if (err == nil) != (signature == "valid") {
				t.Fatalf("probe=%v server=%v\n%s", err, serverErr, log)
			}
			if serverErr != nil {
				t.Fatalf("test exchange: %v\n%s", serverErr, log)
			}
		})
	}
}

// serveTestExchange implements only the unencrypted exchange packets, never a
// production SSH server. Packet encoding follows RFC 4253 section 6; the exchange
// hash and X25519 secret encoding follow RFC 5656 section 4 and RFC 8731 section 3.1.
func serveTestExchange(c net.Conn, signature string) error {
	const version = "SSH-2.0-sandboxed-agents-test"
	if _, err := io.WriteString(c, version+"\r\n"); err != nil {
		return err
	}
	reader := bufio.NewReader(c)
	clientVersion, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	clientVersion = strings.TrimSpace(clientVersion)
	clientInit, err := readTestPacket(reader)
	if err != nil {
		return err
	}
	if clientInit[0] != 20 {
		return fmt.Errorf("expected KEXINIT")
	}
	serverInit := append([]byte{20}, make([]byte, 16)...)
	for _, list := range []string{"curve25519-sha256", "ssh-ed25519", "aes128-ctr", "aes128-ctr", "hmac-sha2-256", "hmac-sha2-256", "none", "none", "", ""} {
		serverInit = append(serverInit, testSSHString([]byte(list))...)
	}
	serverInit = append(serverInit, 0, 0, 0, 0, 0)
	if err := writeTestPacket(c, serverInit); err != nil {
		return err
	}
	init, err := readTestPacket(reader)
	if err != nil {
		return err
	}
	if len(init) != 37 || init[0] != 30 || binary.BigEndian.Uint32(init[1:5]) != 32 {
		return fmt.Errorf("invalid ECDH_INIT")
	}
	clientPublic, err := ecdh.X25519().NewPublicKey(init[5:])
	if err != nil {
		return err
	}
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	secret, err := private.ECDH(clientPublic)
	if err != nil {
		return err
	}
	// RFC 8731 interprets X25519's output bytes as a big-endian unsigned mpint.
	secret = bytes.TrimLeft(secret, "\x00")
	if len(secret) > 0 && secret[0]&0x80 != 0 {
		secret = append([]byte{0}, secret...)
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	hostKey := append(testSSHString([]byte("ssh-ed25519")), testSSHString(pub)...)
	var exchange []byte
	for _, part := range [][]byte{[]byte(clientVersion), []byte(version), clientInit, serverInit, hostKey, clientPublic.Bytes(), private.PublicKey().Bytes(), secret} {
		exchange = append(exchange, testSSHString(part)...)
	}
	hash := sha256.Sum256(exchange)
	signed := ed25519.Sign(key, hash[:])
	if signature == "corrupt" {
		signed[0] ^= 1
	}
	signatureBlob := append(testSSHString([]byte("ssh-ed25519")), testSSHString(signed)...)
	if signature == "absent" {
		signatureBlob = nil
	}
	reply := append([]byte{31}, testSSHString(hostKey)...)
	reply = append(reply, testSSHString(private.PublicKey().Bytes())...)
	reply = append(reply, testSSHString(signatureBlob)...)
	if err := writeTestPacket(c, reply); err != nil {
		return err
	}
	if signature != "valid" {
		return nil
	}
	if err := writeTestPacket(c, []byte{21}); err != nil {
		return err
	}
	// Wait for the client's NEWKEYS before closing, so the real client consumes
	// our complete exchange instead of encountering a reset from unread data.
	_, err = readTestPacket(reader)
	return err
}

// testSSHString encodes bytes as an SSH length-prefixed string for the readiness test server.
func testSSHString(data []byte) []byte {
	out := make([]byte, 4, len(data)+4)
	binary.BigEndian.PutUint32(out, uint32(len(data)))
	return append(out, data...)
}

// writeTestPacket writes an unencrypted SSH transport packet with block-aligned padding for
// readiness tests.
func writeTestPacket(w io.Writer, payload []byte) error {
	padding := 8 - (len(payload)+5)%8
	if padding < 4 {
		padding += 8
	}
	packet := make([]byte, 5+len(payload)+padding)
	binary.BigEndian.PutUint32(packet, uint32(len(packet)-4))
	packet[4] = byte(padding)
	copy(packet[5:], payload)
	_, err := w.Write(packet)
	return err
}

// readTestPacket reads and validates an unencrypted SSH packet and returns its unpadded payload.
func readTestPacket(r io.Reader) ([]byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(header[:4])
	padding := uint32(header[4])
	if size < 2 || size > 65536 || padding+1 >= size {
		return nil, fmt.Errorf("invalid test packet length")
	}
	data := make([]byte, size-1)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, err
	}
	return data[:size-padding-1], nil
}
