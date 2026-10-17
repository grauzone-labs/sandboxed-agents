package main

// The executable's -check mode is an offline walkthrough for this throwaway
// prototype. It exercises the same HTTP forms and TUI keystrokes as a reviewer.
import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"time"
)

func selfCheck() error {
	host := newFakeHost(0)
	handler := browserHandler(host)
	passed := 0
	check := func(ok bool, message string) error {
		if !ok {
			return fmt.Errorf("CHECK FAILED: %s", message)
		}
		passed++
		return nil
	}
	post := func(action, name string, values map[string]string) (int, string) {
		data, _ := json.Marshal(map[string]any{"action": action, "name": name, "values": values})
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/command", strings.NewReader(string(data))))
		return recorder.Code, recorder.Body.String()
	}
	status, _ := post("create", "", map[string]string{"name": "check-sandbox", "start": "true", "chain:dotnet": "true", "agent:codex": "true"})
	if err := check(status == 200 && host.snapshot().findSandbox("check-sandbox").Status == "running", "Create and start a sandbox with toolchain and agent"); err != nil {
		return err
	}
	status, _ = post("create", "", map[string]string{"name": "check-sandbox"})
	if err := check(status == 409 && len(host.snapshot().Sandboxes) == 6, "Duplicate names do not change any sandboxes"); err != nil {
		return err
	}
	status, _ = post("stop", "api-refactor", nil)
	if err := check(status == 409 && host.snapshot().findSandbox("api-refactor").Status == "running", "Active agent session blocks stopping"); err != nil {
		return err
	}
	post("end-sessions", "api-refactor", nil)
	status, _ = post("stop", "api-refactor", nil)
	if err := check(status == 200 && host.snapshot().findSandbox("api-refactor").Status == "stopped", "Stop after ending the session"); err != nil {
		return err
	}
	post("start", "api-refactor", nil)
	before := *host.snapshot().findSandbox("check-sandbox")
	status, _ = post("update", "check-sandbox", map[string]string{"chain:azure": "true", "chain:dotnet": "true"})
	after := *host.snapshot().findSandbox("check-sandbox")
	if err := check(status == 200 && before.Workspace == after.Workspace && slices.Equal(before.Agents, after.Agents) && after.Image == "img-dotnet", "Toolchain change uses the shared image and preserves data"); err != nil {
		return err
	}
	status, _ = post("integration", "docs-agent", map[string]string{"integration": "azure"})
	if err := check(status == 409 && host.snapshot().findSandbox("docs-agent").Integrations["azure"] == "not connected", "Azure without toolchain is rejected"); err != nil {
		return err
	}
	status, _ = post("integration", "check-sandbox", map[string]string{"integration": "azure"})
	if err := check(status == 200 && host.snapshot().findSandbox("check-sandbox").Integrations["azure"] == "connected (demo)", "Azure demo workflow with toolchain"); err != nil {
		return err
	}
	status, _ = post("configure", "check-sandbox", map[string]string{"cpu": "6", "memory": "12", "ssh": "true", "gitName": "Review User", "gitEmail": "review@example.test"})
	b := host.snapshot().findSandbox("check-sandbox")
	if err := check(status == 200 && b.CPU == 6 && b.Memory == 12 && b.SSH && b.GitName == "Review User", "Edit resources, SSH setup and git identity"); err != nil {
		return err
	}
	status, _ = post("agents", "check-sandbox", map[string]string{"agent:claude": "true", "agent:opencode": "true", "agent:codex": "false"})
	if err := check(status == 200 && slices.Equal(host.snapshot().findSandbox("check-sandbox").Agents, []string{"claude", "opencode"}), "Replace agent selection"); err != nil {
		return err
	}
	post("fault", "", nil)
	status, _ = post("stop", "check-sandbox", nil)
	if err := check(status == 409 && host.snapshot().findSandbox("check-sandbox").Status == "running" && len(host.snapshot().Busy) == 0, "Simulated error preserves state and releases the lifecycle lock"); err != nil {
		return err
	}
	status, _ = post("remove", "check-sandbox", map[string]string{"confirm": "wrong"})
	if err := check(status == 409 && host.snapshot().findSandbox("check-sandbox") != nil, "Delete requires exact name confirmation"); err != nil {
		return err
	}
	status, _ = post("remove", "check-sandbox", map[string]string{"confirm": "check-sandbox", "keepVolumes": "true"})
	if err := check(status == 200 && host.snapshot().findSandbox("check-sandbox") == nil && len(host.snapshot().Retained) == 1, "Delete with preserved volumes"); err != nil {
		return err
	}
	post("create", "", map[string]string{"name": "check-sandbox", "start": "true"})
	b = host.snapshot().findSandbox("check-sandbox")
	if err := check(b.GitName == "Review User" && slices.Equal(b.Agents, []string{"claude", "opencode"}) && len(host.snapshot().Retained) == 0, "Re-create adopts preserved home data"); err != nil {
		return err
	}
	post("remove", "check-sandbox", map[string]string{"confirm": "check-sandbox", "keepVolumes": "false"})
	if err := check(host.snapshot().findSandbox("check-sandbox") == nil && len(host.snapshot().Retained) == 0, "Delete including fake volumes"); err != nil {
		return err
	}
	imageCount := len(host.snapshot().Images)
	post("build", "", map[string]string{"chain:azure": "true", "chain:playwright": "true"})
	post("build", "", map[string]string{"chain:azure": "true", "chain:playwright": "true"})
	if err := check(len(host.snapshot().Images) == imageCount+1, "Image build and reuse of identical toolchain sets"); err != nil {
		return err
	}
	for scenario, count := range map[string]int{"empty": 0, "many": 29, "normal": 5} {
		post("scenario", "", map[string]string{"scenario": scenario})
		if err := check(len(host.snapshot().Sandboxes) == count, "Scenario "+scenario); err != nil {
			return err
		}
	}
	// A delayed action must lock its sandbox while unrelated sandboxes progress.
	concurrent := newFakeHost(40 * time.Millisecond)
	reviewHost := newFakeHost(0)
	reviewHandler := browserHandler(reviewHost)
	for _, name := range []string{"", "review-only"} {
		body, _ := json.Marshal(map[string]any{"action": "create", "values": map[string]string{"name": name}})
		recorder := httptest.NewRecorder()
		reviewHandler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/review", strings.NewReader(string(body))))
		want := http.StatusOK
		if name == "" {
			want = http.StatusConflict
		}
		if err := check(recorder.Code == want && reviewHost.snapshot().findSandbox("review-only") == nil && len(reviewHost.snapshot().Events) == 1, "HTTP wizard review validates input without changing sandbox data or events"); err != nil {
			return err
		}
	}
	done := make(chan error, 1)
	go func() { done <- concurrent.command(request{Action: "stop", Name: "web-playground"}) }()
	deadline := time.Now().Add(time.Second)
	for len(concurrent.snapshot().Busy) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	err := concurrent.command(request{Action: "stop", Name: "web-playground"})
	other := concurrent.command(request{Action: "start", Name: "experiment"})
	first := <-done
	if errCheck := check(err != nil && strings.Contains(err.Error(), "Lifecycle lock") && first == nil && other == nil && concurrent.snapshot().findSandbox("experiment").Status == "running" && concurrent.snapshot().findSandbox("web-playground").Status == "stopped", "Lifecycle lock and independent parallel actions"); errCheck != nil {
		return errCheck
	}
	if err := checkWebImageWorkflow(check); err != nil {
		return err
	}
	if err := checkSandboxItems(check); err != nil {
		return err
	}
	if err := checkInstancesAndAttachments(check); err != nil {
		return err
	}
	if err := checkCreationWizard(check); err != nil {
		return err
	}
	fmt.Printf("PASS · %d offline checks · Go fakes, image rollout, HTTP forms and attachments\n", passed)
	return nil
}
