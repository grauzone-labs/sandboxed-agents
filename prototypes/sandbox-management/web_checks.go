package main

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"time"
)

// checkWebImageWorkflow exercises approval and atomic rollout through HTTP,
// alongside the existing executable offline walkthrough.
func checkWebImageWorkflow(check func(bool, string) error) error {
	host := newFakeHost(0)
	handler := ownerFixture(newFakeFleet(host))
	post := func(path, name string, values map[string]string) int {
		body, _ := json.Marshal(map[string]any{"action": "image-toolchains", "name": name, "values": values})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(string(body))))
		return rec.Code
	}
	s := host.snapshot()
	if err := check(len(s.findSandbox("api-refactor").OpenSessions) == 1 && s.findSandbox("api-refactor").OpenSessions[0].Agent == "codex" && len(s.ImageTools["img-dotnet"]) == 5, "Web snapshot lists open sessions by agent and image tool versions"); err != nil {
		return err
	}
	values := map[string]string{"chain:native": "true", "approve": "false", "impact": imageImpact(s, "img-base")}
	status := post("/api/command", "img-base", values)
	if err := check(status == 409 && reflect.DeepEqual(s, host.snapshot()), "Image rollout requires explicit approval and leaves all state untouched when missing"); err != nil {
		return err
	}
	status = post("/api/review", "img-base", values)
	if err := check(status == 200 && reflect.DeepEqual(s, host.snapshot()), "Image impact review has no sandbox, image or event side effects"); err != nil {
		return err
	}
	values["approve"] = "true"
	status = post("/api/command", "img-base", values)
	after := host.snapshot()
	if err := check(status == 200 && after.findSandbox("docs-agent").Image == "img-native" && after.findSandbox("experiment").Image == "img-native" && after.findSandbox("docs-agent").Restarts == 1 && after.findSandbox("experiment").Status == "running" && after.findImage("img-base").ID != "" && reflect.DeepEqual(s.findSandbox("web-playground"), after.findSandbox("web-playground")), "Confirmed shared image change updates and restarts every linked sandbox, reuses an image and preserves unrelated sandboxes"); err != nil {
		return err
	}
	beforeAPI := *after.findSandbox("api-refactor")
	values = map[string]string{"chain:native": "true", "approve": "true", "impact": imageImpact(after, "img-dotnet")}
	status = post("/api/command", "img-dotnet", values)
	b := host.snapshot().findSandbox("api-refactor")
	if err := check(status == 200 && b.Sessions == 0 && len(b.OpenSessions) == 0 && b.Restarts == 1 && b.Workspace == beforeAPI.Workspace && reflect.DeepEqual(b.Agents, beforeAPI.Agents) && reflect.DeepEqual(b.Integrations, beforeAPI.Integrations), "Approved restart ends sessions and preserves workspace, agents and integrations"); err != nil {
		return err
	}
	s = host.snapshot()
	values = map[string]string{"chain:playwright": "true", "approve": "true", "impact": imageImpact(s, "img-native")}
	_ = host.command(request{Action: "stop", Name: "experiment"})
	s = host.snapshot()
	status = post("/api/command", "img-native", values)
	if err := check(status == 409 && reflect.DeepEqual(s, host.snapshot()), "Changed linked sandbox invalidates a stale image confirmation"); err != nil {
		return err
	}
	values["impact"] = imageImpact(s, "img-native")
	_ = host.command(request{Action: "fault"})
	status = post("/api/command", "img-native", values)
	if err := check(status == 409 && host.snapshot().findSandbox("docs-agent").Restarts == s.findSandbox("docs-agent").Restarts && len(host.snapshot().Images) == len(s.Images) && len(host.snapshot().Busy) == 0, "Failed rollout changes no image or sandbox and releases all locks"); err != nil {
		return err
	}
	values["approve"] = "false"
	status = post("/api/review", "img-native", values)
	if err := check(status == 200, "Image draft can be reviewed again after a simulated failure"); err != nil {
		return err
	}
	concurrent := newFakeHost(30 * time.Millisecond)
	before := concurrent.snapshot()
	done := make(chan error, 1)
	go func() {
		done <- concurrent.command(request{Action: "image-toolchains", Name: "img-base", Toolchains: []string{"native"}, Approve: true, Impact: imageImpact(before, "img-base")})
	}()
	deadline := time.Now().Add(time.Second)
	for concurrent.snapshot().Busy["@images"] == "" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	lockErr := concurrent.command(request{Action: "stop", Name: "docs-agent"})
	linkErr := concurrent.command(request{Action: "create", Name: "new-link"})
	otherErr := concurrent.command(request{Action: "start", Name: "release-check"})
	rolloutErr := <-done
	if err := check(lockErr != nil && linkErr != nil && otherErr == nil && rolloutErr == nil && len(concurrent.snapshot().Busy) == 0 && concurrent.snapshot().findSandbox("release-check").Status == "running", "Rollout locks affected sandboxes and image linking while unrelated actions remain available"); err != nil {
		return err
	}
	return nil
}
