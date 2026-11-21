package main_test

import (
	"sync"
	"testing"
)

// liveCase is one independent case run against a private copy of the selected host.
type liveCase struct {
	name string
	body func(*testing.T, *liveSuite)
}

// liveWork limits complete independent cases and initial image work to one budget.
type liveWork struct {
	slots            chan struct{}
	stopAfterFailure bool
	mu               sync.Mutex
	failed           bool
	active           map[*testing.T]struct{}
	joinIndependent  func()
}

// acquire admits work only while the run's failure policy still permits it.
func (work *liveWork) acquire() bool {
	work.slots <- struct{}{}
	if work.stopping() {
		work.release()
		return false
	}
	return true
}

// release makes a slot available only after its admitted work and cleanup finish.
func (work *liveWork) release() { <-work.slots }

// markFailed retains an observed failure after its case leaves the active set.
func (work *liveWork) markFailed() {
	work.mu.Lock()
	work.failed = true
	work.mu.Unlock()
}

// track observes the case through its entire body and cleanup, then retains its
// failure before removing it from the set checked by queued admissions.
func (work *liveWork) track(t *testing.T) {
	work.mu.Lock()
	work.active[t] = struct{}{}
	work.mu.Unlock()
	t.Cleanup(func() {
		if t.Failed() {
			work.markFailed()
		}
		work.mu.Lock()
		delete(work.active, t)
		work.mu.Unlock()
	})
}

// stopping checks thread-safe testing outcomes so nonfatal body and cleanup
// failures stop admission immediately, without cancelling admitted operations.
func (work *liveWork) stopping() bool {
	if !work.stopAfterFailure {
		return false
	}
	work.mu.Lock()
	defer work.mu.Unlock()
	if work.failed {
		return true
	}
	for active := range work.active {
		if active.Failed() {
			return true
		}
	}
	return false
}

// stopping applies the run's failure policy to active cases or serial results.
func (suite *liveSuite) stopping() bool {
	if suite.work != nil {
		return suite.work.stopping()
	}
	suite.stateMu.Lock()
	defer suite.stateMu.Unlock()
	return suite.stopAfterFailure && suite.failed
}

// baseEnsured opens the concurrent phase after lifecycle's existing first up.
func (suite *liveSuite) baseEnsured() {
	if suite.afterBaseUp != nil {
		suite.afterBaseUp()
	}
}

// isolatedCase runs one fixed-name subtest with its own host context and folds
// its result into the run only after all of that case's cleanup has completed.
func (suite *liveSuite) isolatedCase(t *testing.T, item liveCase, configure func(*liveSuite)) {
	local := &liveSuite{host: suite.host, buildDirectory: suite.buildDirectory, recordDirectory: suite.recordDirectory,
		commit: suite.commit, platform: suite.platform, assetHash: suite.assetHash, imagesSelected: suite.imagesSelected,
		results: map[string]string{}, overlap: suite.overlap, buildJobs: suite.buildJobs, work: suite.work}
	if configure != nil {
		configure(local)
	}
	passed := local.run(t, item.name, func(t *testing.T) {
		if local.work != nil {
			local.work.track(t)
		}
		item.body(t, local)
	})
	suite.stateMu.Lock()
	defer suite.stateMu.Unlock()
	suite.results[item.name] = local.results[item.name]
	suite.failed = suite.failed || !passed
	if item.name == "images" {
		suite.imagesRan = local.imagesRan
		suite.imagesComplete = passed
	}
}

// runConcurrent overlaps independent cases and initial toolchain work, then
// joins every case's cleanup before permitting the single image rebuild tail.
// The first case's existing up ensures the base; no preparatory up is added.
// Without an ensured base the remaining cases and images run serially, and the
// failure policy applies before each of them.
func (suite *liveSuite) runConcurrent(t *testing.T, cases []liveCase, images func(*testing.T, *liveSuite)) {
	var independent sync.WaitGroup
	capacity := len(cases)
	if images != nil {
		capacity += len(liveImageSets)
	}
	work := &liveWork{slots: make(chan struct{}, min(suite.overlap, capacity)), stopAfterFailure: suite.stopAfterFailure, active: map[*testing.T]struct{}{}, joinIndependent: independent.Wait}
	suite.work = work
	baseReady := make(chan bool, 1)
	var baseOnce sync.Once
	independent.Add(1)
	go func() {
		defer independent.Done()
		defer baseOnce.Do(func() { baseReady <- false })
		if !work.acquire() {
			return
		}
		defer work.release()
		suite.isolatedCase(t, cases[0], func(local *liveSuite) { local.afterBaseUp = func() { baseOnce.Do(func() { baseReady <- true }) } })
	}()
	if !<-baseReady {
		independent.Wait()
		if suite.stopping() {
			return
		}
		t.Log("base image not ensured; running remaining cases serially")
		suite.work = nil
		for _, item := range cases[1:] {
			if suite.stopping() {
				t.Logf("live case %s not started after an earlier failure", item.name)
				return
			}
			suite.isolatedCase(t, item, nil)
		}
		if images != nil && !suite.stopping() {
			suite.isolatedCase(t, liveCase{"images", images}, nil)
		}
		return
	}
	for _, item := range cases[1:] {
		independent.Add(1)
		go func() {
			defer independent.Done()
			if !work.acquire() {
				t.Logf("live case %s not started after an earlier failure", item.name)
				return
			}
			defer work.release()
			suite.isolatedCase(t, item, nil)
		}()
	}
	var imageRun sync.WaitGroup
	if images != nil {
		imageRun.Add(1)
		go func() {
			defer imageRun.Done()
			if suite.stopping() {
				return
			}
			suite.isolatedCase(t, liveCase{"images", images}, nil)
		}()
	}
	independent.Wait()
	imageRun.Wait()
}
