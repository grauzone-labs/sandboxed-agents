package liverun

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// windowsCommandLineLimit is the CreateProcessW command-line bound, in UTF-16 code units, that each query must stay under.
const windowsCommandLineLimit = 32767

// inventoryEngine answers whole-engine inventory queries for exact requested IDs and records each command line length.
type inventoryEngine struct {
	images      []string
	containers  []string
	inspected   map[string]Container
	commandLens []int
}

// Execute lists every image and container and inspects only the IDs a query names, as Podman does.
func (e *inventoryEngine) Execute(_ context.Context, in Invocation) ([]byte, error) {
	e.commandLens = append(e.commandLens, len(in.Program+" "+strings.Join(in.Args, " ")))
	args := in.Args
	switch {
	case args[0] == "images":
		values := []map[string]string{}
		for _, id := range e.images {
			values = append(values, map[string]string{"Id": id})
		}
		return json.Marshal(values)
	case args[0] == "image" && args[1] == "inspect":
		values := []Image{}
		for _, id := range args[2:] {
			values = append(values, Image{ID: id, Created: time.Date(2026, 11, 18, 1, 0, 0, 0, time.UTC)})
		}
		return json.Marshal(values)
	case args[0] == "ps":
		values := []map[string]string{}
		for _, id := range e.containers {
			values = append(values, map[string]string{"ID": id})
		}
		return json.Marshal(values)
	case args[0] == "container" && args[1] == "inspect":
		values := []Container{}
		for _, id := range args[2:] {
			container, ok := e.inspected[id]
			if !ok {
				container = Container{ID: id, Image: "sha256:" + strings.Repeat("c", 64)}
			}
			values = append(values, container)
		}
		return json.Marshal(values)
	}
	return nil, fmt.Errorf("unexpected process")
}

// TestInventoryBatchesInspectionsUnderWindowsLimit proves the preparation inventory seam inspects 600 images and
// 600 containers completely while keeping every query's command line under the Windows CreateProcessW limit.
func TestInventoryBatchesInspectionsUnderWindowsLimit(t *testing.T) {
	engine := &inventoryEngine{}
	for i := 0; i < 600; i++ {
		engine.images = append(engine.images, fmt.Sprintf("sha256:%064x", i+1))
		engine.containers = append(engine.containers, fmt.Sprintf("%064x", i+1))
	}
	svc := &Service{Executor: engine, Platform: "linux"}
	before := Snapshot{}
	if err := svc.inventory(context.Background(), Options{}, &before); err != nil {
		t.Fatalf("large inventory refused: %v", err)
	}
	if len(before.Images) != 600 || len(before.Containers) != 600 {
		t.Fatalf("inventory incomplete: %d images, %d containers", len(before.Images), len(before.Containers))
	}
	for _, length := range engine.commandLens {
		if length >= windowsCommandLineLimit {
			t.Fatalf("query command line of %d characters exceeds the Windows limit", length)
		}
	}
}

// TestInventoryRequiresEachListedContainerOnce proves the preparation inventory seam refuses a container inspection
// that omits, repeats or substitutes a listed container ID, independent of inspect order, and accepts a reordered answer.
func TestInventoryRequiresEachListedContainerOnce(t *testing.T) {
	first, second := strings.Repeat("1", 64), strings.Repeat("2", 64)
	image := "sha256:" + strings.Repeat("c", 64)
	for _, tc := range []struct {
		name     string
		answer   map[string]Container
		accepted bool
	}{
		{"exact", map[string]Container{}, true},
		{"empty-id", map[string]Container{second: {Image: image}}, false},
		{"duplicate-id", map[string]Container{second: {ID: first, Image: image}}, false},
		{"unexpected-id", map[string]Container{second: {ID: strings.Repeat("3", 64), Image: image}}, false},
		{"reordered", map[string]Container{first: {ID: second, Image: image}, second: {ID: first, Image: image}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := &inventoryEngine{containers: []string{first, second}, inspected: tc.answer}
			svc := &Service{Executor: engine, Platform: "linux"}
			before := Snapshot{}
			err := svc.inventory(context.Background(), Options{}, &before)
			if tc.accepted != (err == nil) {
				t.Fatalf("accepted=%v, got %v", tc.accepted, err)
			}
		})
	}
}

// TestInventoryRefusesDuplicateListedContainer proves the preparation inventory seam refuses a container list that names one ID twice.
func TestInventoryRefusesDuplicateListedContainer(t *testing.T) {
	id := strings.Repeat("1", 64)
	engine := &inventoryEngine{containers: []string{id, id}}
	svc := &Service{Executor: engine, Platform: "linux"}
	before := Snapshot{}
	if err := svc.inventory(context.Background(), Options{}, &before); err == nil {
		t.Fatal("duplicate container list accepted")
	}
}
