package main

import (
	"context"
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"net"
	"net/http"
	"sync"
	"time"
)

// The prototype can host the same service in the TUI process. Disabling its
// listener leaves the engine and terminal history alive for local TUI work.
type embeddedService struct {
	mu     sync.Mutex
	fleet  *fakeFleet
	port   int
	server *http.Server
	conns  map[net.Conn]bool
}

func (s *embeddedService) address() string { return fmt.Sprintf("http://127.0.0.1:%d", s.port) }
func (s *embeddedService) running() bool   { s.mu.Lock(); defer s.mu.Unlock(); return s.server != nil }
func (s *embeddedService) start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.server != nil {
		return nil
	}
	if s.port < 1 || s.port > 65535 {
		return fmt.Errorf("Port must be between 1 and 65535")
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.port))
	if err != nil {
		return err
	}
	s.conns = map[net.Conn]bool{}
	server := &http.Server{Handler: s.fleet.handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	server.ConnState = func(c net.Conn, state http.ConnState) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if state == http.StateNew {
			s.conns[c] = true
		}
		if state == http.StateClosed {
			delete(s.conns, c)
		}
	}
	s.server = server
	go func() { _ = server.Serve(listener) }()
	return nil
}
func (s *embeddedService) stop() error {
	s.mu.Lock()
	server := s.server
	if server == nil {
		s.mu.Unlock()
		return nil
	}
	s.server = nil
	connections := []net.Conn{}
	for c := range s.conns {
		connections = append(connections, c)
	}
	s.mu.Unlock()
	// Shutdown does not close hijacked WebSockets; close those explicitly too.
	for _, c := range connections {
		_ = c.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return server.Shutdown(ctx)
}
func newHostedTUI(host *fakeHost, port int, enabled bool) (*tui, error) {
	m := newTUI(host)
	m.ownedService = &embeddedService{fleet: m.fleet, port: port}
	if !enabled {
		return m, nil
	}
	if err := m.ownedService.start(); err != nil {

		return nil, err
	}
	client, err := newServiceClient(m.ownedService.address(), m.fleet.ownerCredential)
	if err != nil {
		_ = m.ownedService.stop()
		return nil, err
	}
	m.remote = client
	m.applySnapshot(m.fleet.snapshot(m.instance), true)
	return m, nil
}

type serviceChanged struct {
	enabled bool
	err     error
}

func (m *tui) toggleService(enabled bool) tea.Cmd {
	if m.ownedService == nil {
		m.message("This backend is managed by another process.", true)
		return nil
	}
	m.pending = true
	m.pollLoop++
	m.networkRevision++
	m.message("Changing background service availability …", false)
	owner := m.ownedService
	return func() tea.Msg {
		var err error
		if enabled {
			err = owner.start()
		} else {
			err = owner.stop()
		}
		return serviceChanged{enabled, err}
	}
}
func (m *tui) receiveServiceChanged(msg serviceChanged) tea.Cmd {
	m.pending = false
	if msg.err != nil {
		m.message(msg.err.Error(), true)
		if m.remote != nil {
			return m.refreshService()
		}
		return refresh()
	}
	if m.remote != nil {
		m.remote.close()
		m.remote = nil
	}
	if msg.enabled {
		client, err := newServiceClient(m.ownedService.address(), m.fleet.ownerCredential)
		if err != nil {
			m.message(err.Error(), true)
			return refresh()
		}
		m.remote = client
		m.applySnapshot(m.ownedService.fleet.snapshot(m.instance), false)
		m.message("Background service enabled. Web and TUI share the same state.", false)
		return m.refreshService()
	}
	m.fleet = m.ownedService.fleet
	m.host = m.fleet.hosts["local-fake"]
	m.applySnapshot(m.fleet.snapshot(m.instance), false)
	m.online = false
	m.message("Background service disabled. Local TUI data and sessions are kept.", false)
	return refresh()
}
