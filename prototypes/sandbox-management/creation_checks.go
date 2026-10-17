package main

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
)

// checkCreationWizard exercises both image choices, required agents and optional
// integrations through the web API, including review and failed atomic creation.
func checkCreationWizard(check func(bool, string) error) error {
	fleet := newFakeFleet(newFakeHost(0))
	handler := ownerFixture(fleet)
	remoteFleet := newFakeFleet(newFakeHost(0))
	remoteServer := httptest.NewServer(remoteFleet.handler())
	defer remoteServer.Close()
	defer fleet.closeConnections()
	info, err := fleet.changeConnection(connectionRequest{Action: "connect", Label: "Second backend", URL: remoteServer.URL, Token: remoteFleet.ownerCredential})
	if err != nil {
		return err
	}
	post := func(path, instance string, values map[string]string) int {
		body, _ := json.Marshal(map[string]any{"action": "create-sandbox", "values": values})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("POST", path+"?instance="+instance, strings.NewReader(string(body))))
		return rec.Code
	}
	local := fleet.hosts["local-fake"]
	before := local.snapshot()
	status := post("/api/command", "local-fake", map[string]string{"name": "missing-mode"})
	noAgent := post("/api/command", "local-fake", map[string]string{"name": "no-agent", "imageMode": "existing", "imageID": "img-base", "agent:codex": "false"})
	if err := check(status == 409 && noAgent == 409 && reflect.DeepEqual(before, local.snapshot()), "New sandbox requires an explicit image choice and at least one agent"); err != nil {
		return err
	}
	values := map[string]string{"name": "existing-image-box", "imageMode": "existing", "imageID": "img-web", "agent:claude": "true", "agent:codex": "false", "start": "true"}
	status = post("/api/review", "local-fake", values)
	if err := check(status == 200 && reflect.DeepEqual(before, local.snapshot()), "Existing-image review changes no sandbox, image or activity"); err != nil {
		return err
	}
	status = post("/api/command", "local-fake", values)
	b := local.snapshot().findSandbox("existing-image-box")
	if err := check(status == 200 && b.Image == "img-web" && b.Status == "running" && slices.Equal(b.Agents, []string{"claude"}) && len(local.snapshot().Images) == len(before.Images), "Existing-image creation uses the exact selected image without rebuilding"); err != nil {
		return err
	}
	if err := check(b.GitName == "" && b.GitEmail == "" && b.Integrations["github"] == "not connected" && b.Integrations["git"] == "not configured", "Skipping integrations creates a sandbox without configured accounts or Git identity"); err != nil {
		return err
	}
	before = local.snapshot()
	for _, bad := range []map[string]string{
		{"name": "foreign-image", "imageMode": "existing", "imageID": "missing-image"},
		{"name": "changed-image", "imageMode": "existing", "imageID": "img-web", "chain:azure": "true"},
		{"name": "missing-toolchain", "imageMode": "new", "chain:dotnet": "true", "chain:playwright": "true", "connect:azure": "true"},
		{"name": "invalid-git", "imageMode": "new", "chain:dotnet": "true", "chain:playwright": "true", "gitName": "Developer", "gitEmail": "invalid"},
	} {
		status = post("/api/command", "local-fake", bad)
		if err := check(status == 409 && reflect.DeepEqual(before, local.snapshot()), "Invalid image or integration choice leaves all state unchanged: "+bad["name"]); err != nil {
			return err
		}
	}
	values = map[string]string{"name": "new-image-box", "imageMode": "new", "chain:azure": "true", "chain:playwright": "true", "agent:claude": "true", "agent:codex": "false", "gitName": "Wizard User", "gitEmail": "wizard@example.test", "connect:github": "true", "connect:azure": "true", "start": "false"}
	status = post("/api/review", "local-fake", values)
	if err := check(status == 200 && reflect.DeepEqual(before, local.snapshot()), "New-image review can be cancelled without leaving an image or sandbox"); err != nil {
		return err
	}
	_ = local.command(request{Action: "fault"})
	status = post("/api/command", "local-fake", values)
	if err := check(status == 409 && local.snapshot().findSandbox("new-image-box") == nil && len(local.snapshot().Images) == len(before.Images) && len(local.snapshot().Busy) == 0, "Failed image-and-sandbox creation is atomic and releases its lock"); err != nil {
		return err
	}
	status = post("/api/command", "local-fake", values)
	after := local.snapshot()
	b = after.findSandbox("new-image-box")
	if err := check(status == 200 && b != nil && b.Status == "stopped" && len(after.Images) == len(before.Images)+1 && slices.Equal(after.findImage(b.Image).Toolchains, []string{"azure", "playwright"}) && b.GitName == "Wizard User" && b.GitEmail == "wizard@example.test" && b.Integrations["github"] == "connected (demo)" && b.Integrations["azure"] == "connected (demo)", "Confirmation builds the chosen image and applies agent and integration choices to the new sandbox"); err != nil {
		return err
	}
	imageCount := len(after.Images)
	values["name"] = "reuse-built-image"
	status = post("/api/command", "local-fake", values)
	if err := check(status == 200 && len(local.snapshot().Images) == imageCount && local.snapshot().findSandbox("reuse-built-image").Image == b.Image, "An identical toolchain set reuses the matching image"); err != nil {
		return err
	}
	before = local.snapshot()
	status = post("/api/command", info.ID, map[string]string{"name": "remote-wizard-box", "imageMode": "existing", "imageID": "img-dotnet", "connect:azdo": "true"})
	remote := remoteFleet.hosts["local-fake"].snapshot().findSandbox("remote-wizard-box")
	if err := check(status == 200 && remote.Group == "default" && remote.Image == "img-dotnet" && remote.Integrations["azdo"] == "connected (demo)" && reflect.DeepEqual(before, local.snapshot()), "Wizard creation and optional integration setup stay within the selected remote instance"); err != nil {
		return err
	}
	_ = local.command(request{Action: "remove", Name: "experiment", Confirm: "experiment", KeepVolumes: true})
	status = post("/api/command", "local-fake", map[string]string{"name": "experiment", "imageMode": "existing", "imageID": "img-base", "agent:codex": "false", "agent:claude": "true"})
	b = local.snapshot().findSandbox("experiment")
	if err := check(status == 200 && slices.Equal(b.Agents, []string{"claude"}) && b.Workspace == "volume:experiment-workspace", "Reusing retained home data honors the wizard's explicit required agent selection"); err != nil {
		return err
	}
	return nil
}
