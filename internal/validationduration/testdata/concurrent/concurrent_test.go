package concurrent_test

import (
	"sync"
	"testing"
)

// TestLiveSuite proves concurrent fixed t.Run cases and nested parallel image
// units retain distinct completion lines at the real verbose Go output seam.
func TestLiveSuite(t *testing.T) {
	t.Run("preflight", func(t *testing.T) { t.Log("private preflight output") })
	var cases sync.WaitGroup
	for _, name := range []string{"lifecycle", "update", "agent", "t3code", "images"} {
		cases.Add(1)
		go func() {
			defer cases.Done()
			t.Run(name, func(t *testing.T) {
				t.Log("private concurrent output")
				if name == "update" {
					t.Error("scripted failure")
				}
				if name == "images" {
					t.Run("initial", func(t *testing.T) {
						for _, set := range []string{"none", "dotnet", "playwright", "azure", "native"} {
							t.Run(set, func(t *testing.T) { t.Parallel(); t.Log("private image output") })
						}
					})
				}
			})
		}()
	}
	cases.Wait()
}
