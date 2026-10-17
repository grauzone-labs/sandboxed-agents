package main

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
)

//go:embed index.html
var browserHTML string

//go:embed web_assets/*
var browserAssets embed.FS

func browserHandler(host *fakeHost) http.Handler {
	fleet := newFakeFleet(host)
	return fleet.handler()
}

func (fleet *fakeFleet) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/action", func(w http.ResponseWriter, r *http.Request) { fleet.handleAction(w, r, false) })
	mux.HandleFunc("POST /api/action-review", func(w http.ResponseWriter, r *http.Request) { fleet.handleAction(w, r, true) })
	assets, _ := fs.Sub(browserAssets, "web_assets")
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(assets))))
	mux.HandleFunc("GET /api/terminal", fleet.serveTerminal)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write([]byte(browserHTML))
	})
	respond := func(w http.ResponseWriter, value any) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(value)
	}
	resolve := func(w http.ResponseWriter, r *http.Request) (string, *fakeHost) {
		id := r.URL.Query().Get("instance")
		if id == "" {
			id = "local-fake"
		}
		host, ok := fleet.hosts[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			respond(w, map[string]string{"error": "Unknown instance"})
			return "", nil
		}
		return id, host
	}
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		id, host := resolve(w, r)
		if host != nil {
			respond(w, fleet.snapshot(id))
		}
	})
	mux.HandleFunc("GET /api/form", func(w http.ResponseWriter, r *http.Request) {
		_, host := resolve(w, r)
		if host != nil {
			respond(w, makeForm(r.URL.Query().Get("action"), r.URL.Query().Get("name"), host.snapshot()))
		}
	})

	handleForm := func(w http.ResponseWriter, r *http.Request, execute bool) {
		id, host := resolve(w, r)
		if host == nil {
			return
		}
		var input struct {
			Action string            `json:"action"`
			Name   string            `json:"name"`
			Values map[string]string `json:"values"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16384)
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			respond(w, map[string]string{"error": "Invalid input"})
			return
		}
		f := makeForm(input.Action, input.Name, host.snapshot())
		for idx := range f.Fields {
			if value, ok := input.Values[f.Fields[idx].Key]; ok {
				f.Fields[idx].Value = value
			}
		}
		command, err := f.request()
		if command.Action == "image-toolchains" && input.Values["impact"] == "" {
			command.Impact = ""
		}
		if err == nil {
			_, err = fleet.apply(id, command, !execute)
		}
		if err != nil {
			w.WriteHeader(http.StatusConflict)
			respond(w, map[string]string{"error": err.Error()})
			return
		}
		respond(w, fleet.snapshot(id))
	}
	mux.HandleFunc("POST /api/command", func(w http.ResponseWriter, r *http.Request) { handleForm(w, r, true) })
	mux.HandleFunc("POST /api/review", func(w http.ResponseWriter, r *http.Request) { handleForm(w, r, false) })
	return mux
}

func runBrowser(host *fakeHost, port int) error { return runService(host, port) }
