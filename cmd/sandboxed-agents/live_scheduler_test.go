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
	slots chan struct{}
	suite *liveSuite
}

// acquire admits work only while the run's failure policy still permits it.
func (work *liveWork) acquire() bool {
	work.slots <- struct{}{}
	if work.suite.stopping() {
		work.release()
		return false
	}
	return true
}

// release makes a slot available only after its admitted work and cleanup finish.
func (work *liveWork) release() { <-work.slots }

// stopping reads the aggregate failure state without cancelling active cases.
func (suite *liveSuite) stopping() bool {
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
		results: map[string]string{}, overlap: suite.overlap}
	if configure != nil {
		configure(local)
	}
	passed := local.run(t, item.name, func(t *testing.T) {
		defer func() {
			if t.Failed() {
				suite.stateMu.Lock()
				suite.failed = true
				suite.stateMu.Unlock()
			}
		}()
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
func (suite *liveSuite) runConcurrent(t *testing.T, cases []liveCase, images func(*testing.T, *liveSuite)) {
	work := &liveWork{slots: make(chan struct{}, min(suite.overlap, 9)), suite: suite}
	var independent sync.WaitGroup
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
		return
	}
	for _, item := range cases[1:] {
		independent.Add(1)
		go func() {
			defer independent.Done()
			if !work.acquire() {
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
			suite.isolatedCase(t, liveCase{"images", images}, func(local *liveSuite) {
				local.imageWork = work
				local.joinIndependent = independent.Wait
				local.beforeImageRebuild = func(t *testing.T) {
					independent.Wait()
					if suite.stopping() {
						t.Fatal("image coverage incomplete after an earlier case failed")
					}
				}
			})
		}()
	}
	independent.Wait()
	imageRun.Wait()
}
