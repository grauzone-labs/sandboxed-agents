package manager

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/azurebrowser"
	"golang.org/x/sys/unix"
)

// azureSessionAddress validates an opaque capability before naming its ephemeral abstract socket.
func azureSessionAddress(token string) (*net.UnixAddr, error) {
	if !azurebrowser.ValidToken(token) {
		return nil, fmt.Errorf("invalid Azure browser session token")
	}
	return &net.UnixAddr{Name: "@sandboxed-agents-azure-" + token, Net: "unix"}, nil
}

// azureControl requests one session operation and waits for its acknowledgement, without accepting process IDs.
func azureControl(token, operation string, stderr io.Writer, effects Effects) int {
	if !verifyAgent(effects, stderr) {
		return 1
	}
	address, err := azureSessionAddress(token)
	if err != nil || (operation != "start" && operation != "cancel") {
		fmt.Fprintln(stderr, "invalid Azure browser session control")
		return 1
	}
	rawConnection, err := net.DialTimeout("unix", address.Name, time.Second)
	if err != nil {
		fmt.Fprintln(stderr, "Azure browser session control unavailable")
		return 1
	}
	connection := rawConnection.(*net.UnixConn)
	defer connection.Close()
	connection.SetDeadline(time.Now().Add(5 * time.Second))
	if !azureAgentPeer(connection) {
		fmt.Fprintln(stderr, "Azure browser session requires agent peer")
		return 1
	}
	if _, err := fmt.Fprintln(connection, operation); err != nil {
		fmt.Fprintln(stderr, "Azure browser session control failed")
		return 1
	}
	answer, err := bufio.NewReader(io.LimitReader(connection, 4)).ReadString('\n')
	if err != nil || answer != "ok\n" {
		fmt.Fprintln(stderr, "Azure browser session cleanup not acknowledged")
		return 1
	}
	return 0
}

// azureAgentPeer requires the connected process to have the sandbox agent's UID and GID, including on abstract sockets.
func azureAgentPeer(connection *net.UnixConn) bool {
	raw, err := connection.SyscallConn()
	if err != nil {
		return false
	}
	var credentials *unix.Ucred
	var readErr error
	err = raw.Control(func(fd uintptr) {
		credentials, readErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	})
	return err == nil && readErr == nil && credentials != nil && credentials.Uid == 1000 && credentials.Gid == 1000
}

// azureSession owns one browser workflow and acknowledges cancellation only after killing and waiting for its owned children.
func azureSession(token, sshHost string, stdout, stderr io.Writer, effects Effects) int {
	if !verifyAgent(effects, stderr) {
		return 1
	}
	address, err := azureSessionAddress(token)
	if err != nil || !sshAliasPattern.MatchString(sshHost) {
		fmt.Fprintln(stderr, "invalid Azure browser session request")
		return 2
	}
	listener, err := net.ListenUnix("unix", address)
	if err != nil {
		fmt.Fprintln(stderr, "cannot initialize Azure browser session control")
		return 1
	}
	defer listener.Close()
	connections := make(chan *net.UnixConn)
	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		for {
			connection, err := listener.AcceptUnix()
			if err != nil {
				return
			}
			select {
			case connections <- connection:
			case <-stopped:
				connection.Close()
				return
			}
		}
	}()
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(interrupts)
	// No Azure child is permitted until the host has seen readiness and authorized this exact session.
	if _, err := fmt.Fprint(stdout, azurebrowser.SessionPrefix+"\"ready\"\n"); err != nil {
		return 1
	}
	startup := time.NewTimer(10 * time.Second)
	defer startup.Stop()
	cleanupComplete := true
	var cancellation *net.UnixConn
	defer func() {
		// Revoke startup before acknowledging cleanup. A cancelled host may already have
		// closed terminal output, so its independent control connection is the only reply.
		listener.Close()
		if cancellation != nil {
			if cleanupComplete {
				fmt.Fprintln(cancellation, "ok")
			}
			cancellation.Close()
		} else if cleanupComplete {
			fmt.Fprint(stdout, azurebrowser.SessionPrefix+"\"done\"\n")
		}
	}()
startupGate:
	for {
		select {
		case connection := <-connections:
			operation := azureSessionOperation(connection)
			if operation == "cancel" {
				cancellation = connection
				return 1
			}
			if operation != "start" {
				connection.Close()
				continue
			}
			if _, err := fmt.Fprintln(connection, "ok"); err != nil {
				connection.Close()
				return 1
			}
			connection.Close()
			break startupGate
		case <-startup.C:
			fmt.Fprintln(stderr, "Azure browser host did not authorize startup")
			return 1
		case <-interrupts:
			return 1
		}
	}
	stopChild := func(child Child, done <-chan error) {
		if err := child.Signal(os.Kill); err != nil && !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH) {
			cleanupComplete = false
			fmt.Fprintln(stderr, "Azure browser child cleanup failed")
		}
		if err := <-done; errors.Is(err, errProcessGroupCleanup) {
			cleanupComplete = false
		}
	}
	for _, step := range [][]string{{"login"}, {"account", "show"}} {
		child, err := effects.Spawn(Process{Program: "az", Args: step, Identity: agentIdentity, EnvironmentPolicy: AzureEnvironment, AzureSSHHost: sshHost, Stdin: effects.Stdin, Stdout: stdout, Stderr: stderr, OwnProcessGroup: true})
		if err != nil {
			if errors.Is(err, errProcessGroupCleanup) {
				cleanupComplete = false
			}
			fmt.Fprintln(stderr, err)
			return 1
		}
		done := make(chan error, 1)
		go func() { done <- child.Wait() }()
	waitChild:
		for {
			select {
			case err := <-done:
				if err != nil {
					if errors.Is(err, errProcessGroupCleanup) {
						cleanupComplete = false
					}
					fmt.Fprintln(stderr, err)
					return 1
				}
				break waitChild
			case connection := <-connections:
				if azureSessionOperation(connection) != "cancel" {
					connection.Close()
					continue
				}
				cancellation = connection
				stopChild(child, done)
				return 1
			case <-interrupts:
				stopChild(child, done)
				return 1
			}
		}
	}
	return 0
}

// azureSessionOperation accepts only bounded operations from the verified agent peer.
func azureSessionOperation(connection *net.UnixConn) string {
	connection.SetDeadline(time.Now().Add(5 * time.Second))
	if !azureAgentPeer(connection) {
		return ""
	}
	data, err := bufio.NewReader(io.LimitReader(connection, 8)).ReadString('\n')
	if err != nil {
		return ""
	}
	return data[:len(data)-1]
}
