package main_test

import "time"

// liveOperationBounds bounds harness work, leaving production command deadlines unchanged.
// Until per-operation live samples exist, installation bounds use the story's longest
// approximate case/build observations with a wide margin; shorter reads
// retain the suite's existing two-minute Podman budget.
var liveOperationBounds = map[string]time.Duration{
	"preflight":                5 * time.Minute,
	"inspection":               2 * time.Minute,
	"image build":              30 * time.Minute,
	"sandbox start or restart": 10 * time.Minute,
	"SSH server check":         2 * time.Minute,
	"smoke":                    5 * time.Minute,
	"update or rollback":       20 * time.Minute,
	"agent install":            20 * time.Minute,
	"agent session":            5 * time.Minute,
	"rebuild":                  35 * time.Minute,
	"cleanup":                  3 * time.Minute,
	"Tool install":             20 * time.Minute,
	"Tool update":              20 * time.Minute,
	"Tool delete":              5 * time.Minute,
	"Tool probe":               2 * time.Minute,
}

// operationBound returns a class's harness bound, with per-host overrides for fixtures.
func (host liveHost) operationBound(operation string) time.Duration {
	if bound := host.operationBounds[operation]; bound > 0 {
		return bound
	}
	if bound := liveOperationBounds[operation]; bound > 0 {
		return bound
	}
	return 2 * time.Minute
}

// reportOperation emits only fixed class metadata; process output stays in the private log.
func (host liveHost) reportOperation(operation, state string) {
	if host.report != nil {
		host.report("live operation " + operation + ": " + state)
	}
}
