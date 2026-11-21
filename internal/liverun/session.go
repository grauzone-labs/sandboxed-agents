package liverun

import "fmt"

// sessionPolicy obtains fresh process-job facts without changing any host configuration.
func (s *Service) sessionPolicy() (SessionPolicy, error) {
	if s.SessionProbe != nil {
		return s.SessionProbe()
	}
	return sessionJobPolicy()
}

// launchable refuses inheritance when no supported escape from the launching job is available.
func (p SessionPolicy) launchable() error {
	if p.InJob && !p.Breakaway && !p.SilentBreakaway {
		return fmt.Errorf("launching job disallows session breakaway")
	}
	return nil
}
