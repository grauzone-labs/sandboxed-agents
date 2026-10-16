package cli

import (
	"fmt"
	"io"
)

// updateAllSandboxes holds every acquired lifecycle lock until all builds and changes finish.
func updateAllSandboxes(request Request, p podmanTarget, stateRoot string, output io.Writer) error {
	names, err := sandboxInventory(request.Group, p)
	if err != nil {
		return err
	}
	updated, current, volumesOnly, failed := 0, 0, 0, 0
	fail := func(name string, err error) {
		fmt.Fprintf(output, "sandbox %s not updated: %v\n", name, err)
		failed++
	}
	report := func() error {
		fmt.Fprintf(output, "%d updated, %d current, %d volumes only, %d not updated\n", updated, current, volumesOnly, failed)
		if failed > 0 {
			return fmt.Errorf("update --all: %d sandbox(es) not updated", failed)
		}
		return nil
	}
	var plans []*sandboxUpdate
	for _, name := range names {
		request.Name = name
		lock, err := acquireLifecycleLock(stateRoot, request)
		if err != nil {
			fail(name, fmt.Errorf("step 3: %w", err))
			continue
		}
		defer lock.Close()
		objects, err := readSandbox(request, p)
		if err != nil {
			fail(name, err)
			continue
		}
		if err := objects.ownerError(); err != nil {
			fail(name, err)
			continue
		}
		if !objects.exists && !objects.backup {
			if len(objects.volumes) > 0 {
				fmt.Fprintf(output, "sandbox %s skipped: only volumes remain; use up %s to adopt them\n", name, name)
				volumesOnly++
			} else {
				fail(name, fmt.Errorf("sandbox %q does not exist in this controller group", name))
			}
			continue
		}
		if objects.backup {
			plans = append(plans, &sandboxUpdate{request: request, objects: objects})
			continue
		}
		plan, err := prepareSandboxUpdate(request, p, objects)
		if err != nil {
			fail(name, err)
			continue
		}
		if plan == nil {
			fmt.Fprintf(output, "sandbox %s already up to date\n", name)
			current++
			continue
		}
		plans = append(plans, plan)
	}
	// Recovery also changes sandboxes, so it waits behind the same build barrier.
	tags := map[string]string{}
	for _, plan := range plans {
		if plan.objects.backup || tags[toolchainSet(plan.set)] != "" {
			continue
		}
		tag, err := ensureImage(p, plan.set, output)
		if err != nil {
			for _, pending := range plans {
				fail(pending.request.Name, fmt.Errorf("image builds failed; no sandboxes changed: %w", err))
			}
			return report()
		}
		tags[toolchainSet(plan.set)] = tag
	}
	for _, plan := range plans {
		if plan.objects.backup {
			err = recoverUpdate(plan.request, p, plan.objects, output)
		} else {
			err = plan.replace(p, tags[toolchainSet(plan.set)], output)
		}
		if err != nil {
			fail(plan.request.Name, err)
		} else {
			updated++
		}
	}
	return report()
}
