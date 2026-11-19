package preflighttiming

import (
	"math"
	"sort"
	"strings"
)

// Entry joins one harness launch with its producer observation; either may be missing.
type Entry struct {
	Launch     *Launch
	Invocation *Invocation
}

// Journal holds bounded validated observations; vocabulary belongs to the collector.
type Journal struct {
	Entries      []Entry
	InvalidFiles int
}

// validSeconds rejects non-finite or negative observation intervals.
func validSeconds(seconds *float64) bool {
	return seconds != nil && *seconds >= 0 && !math.IsNaN(*seconds) && !math.IsInf(*seconds, 0)
}

// validPhase preserves the distinction between entered-open and returned intervals.
func validPhase(phase *Phase) bool {
	if phase == nil {
		return true
	}
	if phase.State == "open" {
		return phase.Outcome == "" && phase.Seconds == nil
	}
	return phase.State == "closed" && (phase.Outcome == "pass" || phase.Outcome == "fail") && validSeconds(phase.Seconds)
}

// Read joins a bounded channel for a revision; unavailable channels never imply zero work.
func Read(dir, revision string) (Journal, error) {
	var journal Journal
	root, err := openDirectory(dir)
	if err != nil {
		return journal, err
	}
	defer root.Close()
	m, err := readManifest(root)
	if err != nil {
		return journal, err
	}
	if m.Revision != revision {
		return journal, ErrRevision
	}
	names, err := entries(root)
	if err != nil {
		return Journal{InvalidFiles: 1}, nil
	}
	joined := map[string]*Entry{}
	sequences := map[int]bool{}
	sort.Slice(names, func(i, j int) bool { return names[i].Name() < names[j].Name() })
	for _, name := range names {
		filename := name.Name()
		if filename == "manifest.json" || strings.HasPrefix(filename, ".tmp-") {
			continue
		}
		kind, token, ok := strings.Cut(filename, "-")
		token, extension := strings.CutSuffix(token, ".json")
		if !ok || !extension || !tokenPattern.MatchString(token) || kind != "launch" && kind != "inv" {
			journal.InvalidFiles++
			continue
		}
		entry := joined[token]
		if entry == nil {
			entry = &Entry{}
		}
		if kind == "launch" {
			var launch Launch
			if readFile(root, filename, &launch) != nil || launch.Version != 1 || launch.Run != m.Run || launch.Token != token || launch.Sequence < 1 || launch.Sequence > EntryLimit || sequences[launch.Sequence] || len(launch.Case) > 64 || len(launch.Operation) > 64 || (launch.Result == "") != (launch.Seconds == nil) || launch.Result != "" && (launch.Result != "pass" && launch.Result != "fail" || !validSeconds(launch.Seconds)) {
				journal.InvalidFiles++
				continue
			}
			sequences[launch.Sequence] = true
			entry.Launch = &launch
		} else {
			var invocation Invocation
			if readFile(root, filename, &invocation) != nil || invocation.Version != 1 || invocation.Run != m.Run || invocation.Token != token || !validPhase(invocation.Preflight) || !validPhase(invocation.TargetSelection) {
				journal.InvalidFiles++
				continue
			}
			entry.Invocation = &invocation
		}
		joined[token] = entry
	}
	maxSequence := 0
	for seq := range sequences {
		maxSequence = max(maxSequence, seq)
	}
	if maxSequence != len(sequences) {
		journal.InvalidFiles++
	}
	tokens := make([]string, 0, len(joined))
	for token := range joined {
		tokens = append(tokens, token)
	}
	sort.Strings(tokens)
	for _, token := range tokens {
		journal.Entries = append(journal.Entries, *joined[token])
	}
	sort.SliceStable(journal.Entries, func(i, j int) bool {
		left, right := journal.Entries[i].Launch, journal.Entries[j].Launch
		if left == nil {
			return false
		}
		if right == nil {
			return true
		}
		return left.Sequence < right.Sequence
	})
	return journal, nil
}
