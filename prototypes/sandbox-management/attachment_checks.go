package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// checkInstancesAndAttachments exercises independent instance state and real
// WebSocket I/O against in-memory agent sessions, without any SSH or shell.
func checkInstancesAndAttachments(check func(bool, string) error) error {
	fleet := newFakeFleet(newFakeHost(0))
	server := httptest.NewServer(fleet.handler())
	defer server.Close()
	post := func(instance, action, name string, values map[string]string) int {
		body, _ := json.Marshal(map[string]any{"action": action, "name": name, "values": values})
		response, err := http.Post(server.URL+"/api/command?instance="+instance, "application/json", strings.NewReader(string(body)))
		if err != nil {
			return 0
		}
		defer response.Body.Close()
		return response.StatusCode
	}
	local, remote := fleet.snapshot("local-fake"), fleet.snapshot("remote-lab")
	if err := check(local.Instance.ID == "local-fake" && remote.Instance.ID == "remote-lab" && len(remote.Sandboxes) == 4 && remote.findSandbox("payments-api") != nil && local.findImage("remote-web").ID == "" && remote.findImage("img-web").ID == "", "Instances have separate sandbox and image inventories"); err != nil {
		return err
	}
	status := post("remote-lab", "start", "release-check", nil)
	if err := check(status == 200 && fleet.snapshot("remote-lab").findSandbox("release-check").Status == "running" && fleet.snapshot("local-fake").findSandbox("release-check").Status == "stopped", "Same sandbox name is addressed only on the selected instance"); err != nil {
		return err
	}
	status = post("remote-lab", "create", "", map[string]string{"name": "remote-created", "start": "true", "chain:dotnet": "true"})
	created := fleet.snapshot("remote-lab").findSandbox("remote-created")
	if err := check(status == 200 && created != nil && created.Group == "remote-ci" && fleet.snapshot("local-fake").findSandbox("remote-created") == nil && len(fleet.snapshot("local-fake").Images) == len(local.Images), "Remote creation uses its controller group and keeps local inventory unchanged"); err != nil {
		return err
	}
	status = post("unknown", "reset", "", nil)
	if err := check(status == 404 && fleet.snapshot("local-fake").findSandbox("api-refactor") != nil, "Unknown instance cannot fall back to a local mutation"); err != nil {
		return err
	}
	post("local-fake", "fault", "", nil)
	status = post("remote-lab", "stop", "release-check", nil)
	if err := check(status == 200 && fleet.snapshot("local-fake").FaultNext && !fleet.snapshot("remote-lab").FaultNext, "Fault injection belongs only to the selected instance"); err != nil {
		return err
	}
	post("local-fake", "fault", "", nil)
	for scenario, count := range map[string]int{"empty": 0, "many": 28, "normal": 4} {
		status = post("remote-lab", "scenario", "", map[string]string{"scenario": scenario})
		s := fleet.snapshot("remote-lab")
		if err := check(status == 200 && len(s.Sandboxes) == count && s.findImage("remote-web").ID != "" && s.ControllerGroup == "remote-ci" && len(fleet.snapshot("local-fake").Sandboxes) == 5, "Remote scenario "+scenario+" preserves instance identity and local data"); err != nil {
			return err
		}
	}
	post("remote-lab", "reset", "", nil)
	id := fleet.snapshot("remote-lab").findSandbox("payments-api").OpenSessions[0].ID
	dial := func(instance, name, session, origin string) (*websocket.Conn, *http.Response, error) {
		query := url.Values{"instance": {instance}, "sandbox": {name}, "session": {session}}
		return websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/terminal?"+query.Encode(), http.Header{"Origin": {origin}})
	}
	read := func(conn *websocket.Conn) (terminalEvent, error) {
		var event terminalEvent
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		err := conn.ReadJSON(&event)
		return event, err
	}
	conn, response, err := dial("remote-lab", "payments-api", id, "https://untrusted.example")
	if conn != nil {
		conn.Close()
	}
	if response != nil {
		response.Body.Close()
	}
	if e := check(err != nil && response != nil && response.StatusCode == 403 && !fleet.snapshot("remote-lab").findSandbox("payments-api").OpenSessions[0].Attached, "Terminal rejects a foreign browser origin without marking the session attached"); e != nil {
		return e
	}
	conn, response, err = dial("local-fake", "payments-api", id, server.URL)
	if conn != nil {
		conn.Close()
	}
	if response != nil {
		response.Body.Close()
	}
	if e := check(err != nil && response != nil && response.StatusCode == 409, "Terminal rejects a session from another instance"); e != nil {
		return e
	}
	conn, _, err = dial("remote-lab", "payments-api", id, server.URL)
	if err != nil {
		return err
	}
	defer conn.Close()
	initial, err := read(conn)
	if e := check(err == nil && strings.Contains(initial.Data, "remote-lab") && strings.Contains(initial.Data, "payments-api") && fleet.snapshot("remote-lab").findSandbox("payments-api").OpenSessions[0].Attached, "Attach opens a real WebSocket stream for the selected remote demo session"); e != nil {
		return e
	}
	if err = conn.WriteJSON(map[string]any{"type": "input", "data": "remote-only prompt\r"}); err != nil {
		return err
	}
	reply, err := read(conn)
	if e := check(err == nil && strings.Contains(reply.Data, "remote-only prompt") && strings.Contains(reply.Data, "simulated response"), "Terminal accepts input and streams the demo agent response"); e != nil {
		return e
	}
	conn.Close()
	deadline := time.Now().Add(time.Second)
	for fleet.snapshot("remote-lab").findSandbox("payments-api").OpenSessions[0].Attached && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if e := check(!fleet.snapshot("remote-lab").findSandbox("payments-api").OpenSessions[0].Attached && fleet.snapshot("remote-lab").findSandbox("payments-api").Sessions == 1, "Closing the attachment detaches its client and keeps the agent session running"); e != nil {
		return e
	}
	rejoined, _, err := dial("remote-lab", "payments-api", id, server.URL)
	if err != nil {
		return err
	}
	defer rejoined.Close()
	history, err := read(rejoined)
	if e := check(err == nil && strings.Contains(history.Data, "remote-only prompt"), "Reattachment restores the existing terminal history"); e != nil {
		return e
	}
	localID := fleet.snapshot("local-fake").findSandbox("api-refactor").OpenSessions[0].ID
	localConn, _, err := dial("local-fake", "api-refactor", localID, server.URL)
	if err != nil {
		return err
	}
	defer localConn.Close()
	localHistory, err := read(localConn)
	if e := check(err == nil && !strings.Contains(localHistory.Data, "remote-only prompt") && strings.Contains(localHistory.Data, "local-fake"), "Local and remote terminal histories are isolated"); e != nil {
		return e
	}
	status = post("remote-lab", "session-end", "payments-api", map[string]string{"session": id})
	ended, err := read(rejoined)
	if e := check(status == 200 && err == nil && ended.Type == "ended" && fleet.snapshot("local-fake").findSandbox("api-refactor").Sessions == 1, "Ending the remote session closes its terminal stream and preserves local sessions"); e != nil {
		return e
	}
	status = post("local-fake", "reset", "", nil)
	ended, err = read(localConn)
	if e := check(status == 200 && err == nil && ended.Type == "ended" && strings.Contains(ended.Data, "reset"), "Reset closes attachments and clears only the selected instance terminal history"); e != nil {
		return e
	}
	return nil
}
