package main

// THROWAWAY: one process owns both fake instances and all terminal sessions.
// Clients can come and go without resetting that process's state.
import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type serviceInfo struct {
	ID               string `json:"id"`
	Started          string `json:"started"`
	State            string `json:"state"`
	PollMilliseconds int    `json:"pollMilliseconds"`
}

func newServiceInfo() serviceInfo {
	var id [8]byte
	_, _ = rand.Read(id[:])
	return serviceInfo{hex.EncodeToString(id[:]), time.Now().UTC().Format(time.RFC3339), "Shared fake state in service memory", 300}
}
func (f *fakeFleet) apply(id string, r request, review bool) (instanceState, error) {
	host := f.hosts[id]
	if host == nil {
		return instanceState{}, fmt.Errorf("Unknown instance")
	}
	if review {
		candidate := host.snapshot()
		if r.Action == "image-toolchains" {
			r.Approve = true
		}
		if _, err := applyRequest(&candidate, r); err != nil {
			return instanceState{}, err
		}
	} else {
		if err := host.command(r); err != nil {
			return instanceState{}, err
		}
		if r.Action == "reset" || r.Action == "scenario" {
			f.terminals.reset(id)
		} else {
			f.terminals.reconcile(id, host)
		}
	}
	return f.snapshot(id), nil
}
func (f *fakeFleet) handleAction(w http.ResponseWriter, r *http.Request, review bool) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	respondError := func(code int, err error) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
	}
	id := r.URL.Query().Get("instance")
	if id == "" {
		id = "local-fake"
	}
	if f.hosts[id] == nil {
		respondError(404, fmt.Errorf("Unknown instance"))
		return
	}
	var command request
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&command); err != nil {
		respondError(400, fmt.Errorf("Invalid command"))
		return
	}
	if command.Action == "create" && command.ImageMode == "" {
		respondError(409, fmt.Errorf("Select an existing image or create a new image"))
		return
	}
	state, err := f.apply(id, command, review)
	if err != nil {
		respondError(409, err)
		return
	}
	_ = json.NewEncoder(w).Encode(state)
}
func runService(host *fakeHost, port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("Port must be between 1 and 65535")
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return err
	}
	fleet := newFakeFleet(host)
	fmt.Printf("Backend service: http://127.0.0.1:%d/\nWeb Control room: http://127.0.0.1:%d/\nService ID: %s\nShared fake state stays alive when clients close; restarting the service resets it.\nCtrl+C stops the service.\n", port, port, fleet.service.ID)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := &http.Server{Handler: fleet.handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		select {
		case <-ctx.Done():
			for _, i := range fleet.instances {
				fleet.terminals.reset(i.ID)
			}
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdown)
		case <-stopped:
		}
	}()
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
