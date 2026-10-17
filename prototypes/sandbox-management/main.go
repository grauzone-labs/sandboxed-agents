package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	serve := flag.Bool("serve", false, "Run the backend service and web UI without a TUI")
	connect := flag.String("connect", "", "Connect the TUI to an existing backend URL")
	service := flag.Bool("service", true, "Start web/API sharing with the TUI; toggle it in Configuration")
	browser := flag.Bool("browser", false, "Start the web Control room on http://127.0.0.1:4317")
	port := flag.Int("port", 4317, "Port for the local web Control room")
	scenario := flag.String("scenario", "normal", "Fake data set: normal, empty, many")
	delay := flag.Duration("delay", 650*time.Millisecond, "Simulated action duration, e.g. 2s")
	check := flag.Bool("check", false, "Offline fake/API checks; run go test for TUI keyboard checks")
	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(), `SANDBOXED AGENTS · THROWAWAY PROTOTYPE

Question: Does the web Control room work well as a keyboard-first TUI?

Start from the prototype directory:
  go run .                     TUI + automatic background service on port 4317
  go run . -service=false       TUI with sharing initially disabled
  go run . -serve               Independent backend + web UI
  go run . -connect http://127.0.0.1:4317   Connect another TUI
  go run . -browser            Web Control room
  go run . -check              Offline fake/API checks
  go test ./...                TUI keyboard and workflow checks
  go build -o sandbox-tui .    Build a single executable

Controls:
  1 Sandboxes · 2 Images · 3 Application configuration · g Instance
  ↑/↓ Object · ←/→ Sandbox tab · Tab Available action · Enter Run
  n New sandbox wizard · b Build image · / Search · v Status
  a Agents · i Integrations · c Sandbox configuration · e Sessions
  t Image · s Start · x Stop · d Delete · ? Help · q Quit
  Forms: Tab field · Space toggle · ←/→ choice · Ctrl+S Next/Apply
  Wizard: F2 Image source · Ctrl+K Skip integrations · Esc Back
  Ctrl+G Cancel · Session terminal: Esc or Ctrl+D Detach
  f Next action fails · p Scenario normal/empty/many · r Reset

All changes stay in memory. Each instance owns independent fake data.
No real sandboxes, images, SSH connections or credentials are touched.
Versions are illustrative; interactive login is future work.
The prototype is disabled when SANDBOX_PROTOTYPE_ENV=production.

Options:
`)
		flag.PrintDefaults()
	}
	flag.Parse()
	if os.Getenv("SANDBOX_PROTOTYPE_ENV") == "production" {
		fmt.Fprintln(os.Stderr, "Throwaway prototype is disabled in production mode")
		os.Exit(1)
	}
	if *delay < 0 || *delay > time.Minute {
		fmt.Fprintln(os.Stderr, "delay must be between 0 and 1m")
		os.Exit(1)
	}
	if *check {
		if err := selfCheck(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	host := newFakeHost(*delay)
	if err := host.command(request{Action: "scenario", Scenario: *scenario}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *browser || *serve {
		if err := runBrowser(host, *port); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	var model *tui
	var err error
	if *connect != "" {
		var client *serviceClient
		client, err = newServiceClient(*connect)
		if err == nil {
			model = newConnectedTUI(client, "local-fake")
		}
	} else {
		model, err = newHostedTUI(host, *port, *service)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer model.close()
	program := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := program.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
