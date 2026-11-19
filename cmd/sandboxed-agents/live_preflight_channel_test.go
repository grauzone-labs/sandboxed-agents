package main_test

import (
	"strings"

	"github.com/grauzone-labs/sandboxed-agents/internal/preflighttiming"
)

// timingEnvironment clones a case host's environment and gives only this child
// its fresh ledger token; disabled or invalid channels clear inherited credentials.
func (host liveHost) timingEnvironment(operation string) ([]string, string) {
	environment := make([]string, 0, len(host.environment)+2)
	for _, entry := range host.environment {
		key, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(key, preflighttiming.TokenEnv) || host.ledger == nil && strings.EqualFold(key, preflighttiming.DirEnv) {
			continue
		}
		environment = append(environment, entry)
	}
	if host.ledger == nil {
		return environment, ""
	}
	entry, token, _ := host.ledger.Launch(host.caseName, operation)
	if entry != "" {
		environment = append(environment, entry)
	}
	return environment, token
}

// finishTiming records inclusive process results without exposing errors or output.
func (host liveHost) finishTiming(token string, seconds float64, err error) {
	result := "pass"
	if err != nil {
		result = "fail"
	}
	host.ledger.Finish(token, result, seconds)
}
