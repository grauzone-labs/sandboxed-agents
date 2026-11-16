// Package gitcredentials owns the encrypted Git credential store and its nonprompting IPC adapters.
package gitcredentials

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"
)

const secretName = "sandboxed-agents.git-credentials.v1"
const socketDeadline = 2 * time.Second

// agentConnection owns one deadline-bound Assuan connection; errors never include peer data.
type agentConnection struct {
	net.Conn
	reader   *bufio.Reader
	deadline time.Time
}

// connectAgent opens the private socket without starting or prompting an agent.
func connectAgent(socket string) (*agentConnection, error) {
	return connectAgentBefore(socket, time.Time{})
}

// connectAgentBefore caps socket connection and exchanges at the shared helper deadline.
func connectAgentBefore(socket string, deadline time.Time) (*agentConnection, error) {
	limit := socketLimit(deadline)
	if !time.Now().Before(limit) {
		return nil, errHelperDeadline
	}
	c, err := net.DialTimeout("unix", socket, time.Until(limit))
	if err != nil {
		return nil, errors.New("Git credential store is locked or its agent is unavailable")
	}
	c.SetDeadline(socketLimit(deadline))
	a := &agentConnection{Conn: c, reader: bufio.NewReader(c), deadline: deadline}
	if _, err := a.response(nil); err != nil {
		c.Close()
		return nil, err
	}
	return a, nil
}

// escapeAssuan quotes data and command separators without changing spaces or plus signs on decoding.
func escapeAssuan(value string) string {
	return strings.NewReplacer("%", "%25", "\n", "%0A", "\r", "%0D", "\x00", "%00", " ", "%20", "+", "%2B").Replace(value)
}

// response consumes data and inquiries through the terminating Assuan status.
func (a *agentConnection) response(passphrase []byte) ([]byte, error) {
	var result []byte
	for {
		line, err := a.reader.ReadString('\n')
		if err != nil {
			return nil, errors.New("Git credential agent did not answer")
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		switch {
		case line == "OK" || strings.HasPrefix(line, "OK "):
			return result, nil
		case strings.HasPrefix(line, "ERR"):
			return nil, errors.New("Git credential agent refused the request")
		case strings.HasPrefix(line, "D "):
			data, err := url.PathUnescape(line[2:])
			if err != nil {
				return nil, errors.New("invalid Git credential agent data")
			}
			result = append(result, data...)
		case strings.HasPrefix(line, "INQUIRE "):
			if passphrase != nil && (strings.HasPrefix(line, "INQUIRE PASSPHRASE") || strings.HasPrefix(line, "INQUIRE NEW_PASSPHRASE")) {
				if _, err := fmt.Fprintf(a, "D %s\nEND\n", escapeAssuan(string(passphrase))); err != nil {
					return nil, errors.New("Git credential agent write failed")
				}
			} else {
				io.WriteString(a, "CAN\n")
			}
		}
	}
}

// command exchanges a private command, suppressing all peer diagnostics.
func (a *agentConnection) command(command string, passphrase []byte) ([]byte, error) {
	a.SetDeadline(socketLimit(a.deadline))
	if _, err := io.WriteString(a, command+"\n"); err != nil {
		return nil, errors.New("Git credential agent write failed")
	}
	return a.response(passphrase)
}

// Pinentry serves pinentry Assuan requests without any terminal access or user interaction.
func Pinentry(input io.Reader, output io.Writer, socket string) error {
	if _, err := io.WriteString(output, "OK sandboxed-agents pinentry\n"); err != nil {
		return err
	}
	scanner := bufio.NewScanner(input)
	for scanner.Scan() {
		command, _, _ := strings.Cut(scanner.Text(), " ")
		switch command {
		case "BYE":
			_, err := io.WriteString(output, "OK\n")
			return err
		case "GETPIN":
			a, err := connectAgent(socket)
			var secret []byte
			if err == nil {
				secret, err = a.command("GET_SECRET "+secretName, nil)
				a.Close()
			}
			if err != nil || len(secret) == 0 {
				io.WriteString(output, "ERR 83886179 Git credential store is locked\n")
				continue
			}
			_, err = fmt.Fprintf(output, "D %s\nOK\n", escapeAssuan(string(secret)))
			clear(secret)
			if err != nil {
				return err
			}
		case "OPTION", "SETDESC", "SETPROMPT", "SETTITLE", "SETOK", "SETCANCEL", "SETERROR", "SETKEYINFO", "SETQUALITYBAR", "SETQUALITYBAR_TT", "SETREPEAT", "SETREPEATERROR", "RESET":
			io.WriteString(output, "OK\n")
		case "GETINFO":
			io.WriteString(output, "D sandboxed-agents\nOK\n")
		default:
			io.WriteString(output, "ERR 83886179 Interactive pinentry is disabled\n")
		}
	}
	return scanner.Err()
}

// socketLimit takes the earlier of the socket timeout and whole-helper deadline.
func socketLimit(deadline time.Time) time.Time {
	limit := time.Now().Add(socketDeadline)
	if !deadline.IsZero() && deadline.Before(limit) {
		return deadline
	}
	return limit
}
