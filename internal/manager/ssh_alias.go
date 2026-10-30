package manager

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// SSH alias changes query the trusted OpenSSH client, bound what they keep of its output, its diagnostics, the configuration
// sources they read, and each answer, and serialize on a lock directory.
const (
	sshProgram = "/usr/bin/ssh"
	// sshQueryLimit bounds the ssh -G option listing a query keeps from standard output; a successful query with a longer
	// listing is refused.
	sshQueryLimit = 64 << 10
	// sshDiagnosticLimit bounds the diagnostics a query keeps from standard error; a successful query with longer diagnostics
	// is refused, and a failing one reports only what was kept. -vvv reports several lines for every Host and Match block,
	// so a long configuration needs far more room than its option listing.
	sshDiagnosticLimit = 1 << 20
	// sshSourceLimit bounds how much of an included configuration source readSSHSource reads to inspect an applied block;
	// a longer source is not inspected.
	sshSourceLimit = 1 << 20
	// sshAnswerLimit exceeds any valid alias, so a longer answer is refused as invalid.
	sshAnswerLimit = 1024
	// sshCandidateConfig makes ssh -G read the configuration it would see after the write from the query's standard input.
	sshCandidateConfig = "/dev/stdin"
	// sshConfigLock serializes changes to the agent's SSH configuration across concurrent workflows.
	sshConfigLock = ".ssh/.config.lock"
	// sshControlAlias is a host name that sshAliasName never accepts, so no chosen alias equals it; placeholder probes
	// resolve it beside the alias to tell settings every alias gets from settings specific to the alias.
	sshControlAlias = "_sandboxed-agents-control"
	// sshProbePlaceholder begins the placeholder values of the trailing Host * block that placeholder probes append.
	sshProbePlaceholder = "sandboxed-agents-probe-"
)

// sshAliasName admits one literal Host pattern: no spaces, wildcards, negation, tokens, or leading dash. It allows up to
// 253 characters, or 254 when the last one is the DNS root dot of a fully qualified machine name.
var sshAliasName = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]{0,252}|[A-Za-z0-9._-]{252}\.)$`)

// sshEntryOption is one option the host entry sets: its ssh -G key, its ssh_config keyword, whether the keyword
// expands % tokens, so its value needs %% escapes, and whether OpenSSH collects every value that applies, as for
// IdentityFile, instead of keeping the first one it reads.
type sshEntryOption struct {
	key, keyword     string
	tokens, collects bool
}

// sshEntryOptions lists, in entry order, every option the host entry sets; the entry lines, the values checked through
// ssh -G, and the placeholder probes are all built from it.
var sshEntryOptions = []sshEntryOption{
	{key: "hostname", keyword: "HostName", tokens: true},
	{key: "user", keyword: "User"},
	{key: "identityfile", keyword: "IdentityFile", tokens: true, collects: true},
	{key: "identitiesonly", keyword: "IdentitiesOnly"},
	{key: "userknownhostsfile", keyword: "UserKnownHostsFile", tokens: true},
}

// sshEntryValues keys values, given one per entry option in sshEntryOptions order, by each option's ssh -G key. It panics
// when the number of values differs from the number of entry options, which only a programming error causes.
func sshEntryValues(values ...string) map[string]string {
	if len(values) != len(sshEntryOptions) {
		panic(fmt.Sprintf("sshEntryValues: %d values for %d entry options", len(values), len(sshEntryOptions)))
	}
	keyed := make(map[string]string, len(values))
	for i, option := range sshEntryOptions {
		keyed[option.key] = values[i]
	}
	return keyed
}

// configureSSHAlias chooses the alias for a normalized outbound SSH target and adds its host entry, with the remote
// username, the dedicated key keyName in ~/.ssh, IdentitiesOnly, and the agent's ~/.ssh/known_hosts, to ~/.ssh/config.
// A machine name is its own alias and is used without a question; one that sshAliasName refuses fails without a
// question and changes nothing. For an address it asks on stderr and reads one answer line from Effects.Stdin without
// consuming later input, and asks again after an answer that sshAliasName refuses. An alias that a specific SSH
// configuration block claims, as sshAliasClaimed decides, is reported with the options that block sets and asked again;
// end of input cancels. When an earlier setting that matches the alias, such as one under Host * or before the first
// Host line, would give it another value for an entry option, or another key beside the dedicated IdentityFile, it
// fails without asking again and names that option, as checkSSHEntry decides. It verifies the agent identity before
// reading home data and queries ssh -G as agent. It writes no host entry and leaves existing configuration content
// unchanged unless it returns the chosen alias; a missing .ssh directory is created, mode 0700, only after an alias is
// chosen and its ssh -G check succeeds, so a cancelled question or a failing query creates nothing, and a held
// configuration lock refuses the change. It runs only after the successful password test (ADR-0011); #112 supplies that
// caller, which does not exist yet.
func configureSSHAlias(target, user, keyName string, stderr io.Writer, effects Effects) (string, error) {
	if !verifyAgent(effects, stderr) {
		return "", fmt.Errorf("SSH alias requires the agent identity; SSH configuration unchanged")
	}
	if effects.HomeDirectory == nil {
		return "", fmt.Errorf("SSH home data is unavailable")
	}
	home, err := effects.HomeDirectory()
	if err != nil || !filepath.IsAbs(home) {
		return "", fmt.Errorf("SSH home data is unavailable")
	}
	// #109 keeps confirmed host keys in the agent's usual known_hosts; the entry names it so the alias never depends on another default.
	want := sshEntryValues(target, user, filepath.Join(home, ".ssh", keyName), "yes", filepath.Join(home, ".ssh", "known_hosts"))
	lines, err := sshOptionLines(want)
	if err != nil {
		return "", fmt.Errorf("SSH host entry values cannot be written safely; SSH configuration unchanged")
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return "", fmt.Errorf("cannot open SSH home data")
	}
	defer root.Close()
	// Refuse unusable existing files before asking anything; the write repeats the checks under the lock.
	if _, err := root.Lstat(".ssh"); err == nil {
		if err := checkSSHDirectory(root); err != nil {
			return "", err
		}
		if _, err := readSSHConfig(root); err != nil {
			return "", err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("cannot inspect SSH directory")
	}
	stdin := effects.Stdin
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	// An address has no readable name, so the user picks the alias; a machine name is the alias and is never replaced by a question.
	alias := target
	if _, err := netip.ParseAddr(target); err == nil {
		alias = ""
	} else if !sshAliasName.MatchString(target) {
		return "", fmt.Errorf("machine name %s is not a valid SSH alias; an alias uses letters, digits, dots, dashes, or underscores, starts with a letter or digit, and has at most 253 characters plus a final dot; SSH configuration unchanged", target)
	}
	for {
		if alias == "" {
			if alias, err = askSSHAlias(stdin, target, stderr); err != nil {
				return "", err
			}
		}
		if !sshAliasName.MatchString(alias) {
			fmt.Fprintln(stderr, "invalid SSH alias name; use letters, digits, dots, dashes, or underscores, starting with a letter or digit")
			alias = ""
			continue
		}
		claimed, err := addSSHAlias(root, alias, "Host "+alias+"\n"+lines, want, effects)
		if err != nil {
			return "", err
		}
		if len(claimed) == 0 {
			return alias, nil
		}
		fmt.Fprintf(stderr, "SSH alias %s is already claimed by a specific SSH configuration block that sets %s; nothing written, choose another alias name\n", alias, strings.Join(claimed, ", "))
		alias = ""
	}
}

// sshOptionLines returns the indented ssh_config lines that set each entry option to its value in values, keyed by
// ssh -G key and escaped so OpenSSH reads each value back unchanged, or an error when a value cannot be written safely.
func sshOptionLines(values map[string]string) (string, error) {
	var lines strings.Builder
	for _, option := range sshEntryOptions {
		value, err := sshConfigValue(values[option.key], option.tokens)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&lines, "    %s %s\n", option.keyword, value)
	}
	return lines.String(), nil
}

// addSSHAlias adds the entry to the configuration in the home that root opens and returns nothing, or writes nothing and
// returns the option keywords, as sshAliasClaimed reports them, of a specific SSH configuration block that claims the
// alias. It holds the configuration lock across the claim queries, the check of the resulting
// configuration, and the atomic write. Without a .ssh directory it checks the entry alone first, so a failing query
// creates nothing, and it queries no more once the lock confirms there is still no configuration file.
func addSSHAlias(root *os.Root, alias, entry string, want map[string]string, effects Effects) ([]string, error) {
	_, err := root.Lstat(".ssh")
	entryAloneChecked := errors.Is(err, os.ErrNotExist)
	if entryAloneChecked {
		if err := checkSSHEntry([]byte(entry), alias, want, effects); err != nil {
			return nil, err
		}
	}
	if err := prepareSSHDirectory(root); err != nil {
		return nil, err
	}
	if err := root.Mkdir(sshConfigLock, 0700); err != nil {
		return nil, fmt.Errorf("another SSH configuration change is in progress, or its lock %s was left behind; retry after it finishes or remove that lock; SSH configuration unchanged", filepath.Join(root.Name(), sshConfigLock))
	}
	defer root.Remove(sshConfigLock)
	current, err := readSSHConfig(root)
	if err != nil {
		return nil, err
	}
	if current.exists {
		claimed, err := sshAliasClaimed(root, filepath.Join(root.Name(), ".ssh", "config"), current.content, alias, effects)
		if err != nil || len(claimed) > 0 {
			return claimed, err
		}
		entryAloneChecked = false
	}
	content := appendSSHEntry(current.content, entry)
	if !entryAloneChecked {
		if err := checkSSHEntry(content, alias, want, effects); err != nil {
			return nil, err
		}
	}
	return nil, replaceSSHConfig(root, current.mode, content)
}

// sshConfigFile is the agent's ~/.ssh/config as read under the lock: whether it exists, its bytes, and the mode a replacement keeps.
type sshConfigFile struct {
	exists  bool
	content []byte
	mode    os.FileMode
}

// readSSHConfig reads ~/.ssh/config, or reports it absent with the 0600 mode a new file receives, and refuses a file that is
// not regular, is not owned by agent, or is writable by group or others, as OpenSSH does.
func readSSHConfig(root *os.Root) (sshConfigFile, error) {
	relative := filepath.Join(".ssh", "config")
	info, err := root.Lstat(relative)
	if errors.Is(err, os.ErrNotExist) {
		return sshConfigFile{mode: 0600}, nil
	}
	if err != nil {
		return sshConfigFile{}, fmt.Errorf("cannot inspect SSH configuration")
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || !sshFileOwned(info) {
		return sshConfigFile{}, fmt.Errorf("SSH configuration must be a regular file owned by agent and not writable by others; SSH configuration unchanged")
	}
	content, err := root.ReadFile(relative)
	if err != nil {
		return sshConfigFile{}, fmt.Errorf("cannot read SSH configuration; SSH configuration unchanged")
	}
	return sshConfigFile{exists: true, content: content, mode: info.Mode().Perm()}, nil
}

// appendSSHEntry returns the previous configuration bytes, unchanged, followed by the entry; earlier content without a final
// newline gains one, and a blank line separates the entry from it.
func appendSSHEntry(previous []byte, entry string) []byte {
	if len(previous) == 0 {
		return []byte(entry)
	}
	content := append([]byte{}, previous...)
	if content[len(content)-1] != '\n' {
		content = append(content, '\n')
	}
	return append(append(content, '\n'), entry...)
}

// askSSHAlias asks for an alias name and reads one answer line; end of input cancels without writing.
func askSSHAlias(stdin io.Reader, target string, stderr io.Writer) (string, error) {
	fmt.Fprintf(stderr, "SSH alias name for %s: ", target)
	line, err := readSSHAnswer(stdin)
	if err != nil {
		return "", fmt.Errorf("no SSH alias name given; SSH configuration unchanged")
	}
	return strings.TrimSpace(line), nil
}

// readSSHAnswer reads one line one byte at a time, so input after its newline stays unread for the caller.
// It keeps at most sshAnswerLimit bytes of a longer line and returns a final line without newline at end of input.
func readSSHAnswer(stdin io.Reader) (string, error) {
	var line []byte
	next := make([]byte, 1)
	for {
		n, err := stdin.Read(next)
		if n == 1 {
			if next[0] == '\n' {
				return string(line), nil
			}
			if len(line) < sshAnswerLimit {
				line = append(line, next[0])
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) && len(line) > 0 {
				return string(line), nil
			}
			return "", err
		}
	}
}

// sshConfigValue escapes one ssh_config argument so OpenSSH reads it back unchanged:
// backslashes always, and percent signs where the keyword expands tokens. It refuses
// whitespace, control characters, quotes, and comment marks instead of quoting them.
func sshConfigValue(value string, tokens bool) (string, error) {
	if value == "" || strings.ContainsFunc(value, func(r rune) bool { return r <= ' ' || r == 0x7f || strings.ContainsRune("\"'#", r) }) {
		return "", fmt.Errorf("unsafe SSH configuration value")
	}
	value = strings.ReplaceAll(value, `\`, `\\`)
	if tokens {
		value = strings.ReplaceAll(value, "%", "%%")
	}
	return value, nil
}

// replaceSSHConfig writes content to a new file in .ssh with the given mode and renames it over the configuration, so readers
// see the old or the new file and a failed write leaves the old one intact. The temporary file is removed when any step fails.
func replaceSSHConfig(root *os.Root, mode os.FileMode, content []byte) error {
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return fmt.Errorf("cannot name a temporary SSH configuration; SSH configuration unchanged")
	}
	temporary := filepath.Join(".ssh", fmt.Sprintf(".config.%x.tmp", suffix))
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("cannot create a temporary SSH configuration; SSH configuration unchanged")
	}
	_, writeErr := file.Write(content)
	if writeErr == nil {
		writeErr = file.Chmod(mode)
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr == nil {
		writeErr = root.Rename(temporary, filepath.Join(".ssh", "config"))
	}
	if writeErr != nil {
		root.Remove(temporary)
		return fmt.Errorf("cannot write SSH configuration; SSH configuration unchanged")
	}
	return nil
}

// sshAliasClaimed returns what claims the alias: first, in entry order, the keywords of the entry options that a block
// specific to the alias explicitly sets, then the other keywords that a literal Host block for the alias sets, and nothing
// when no block claims it. OpenSSH's -vvv diagnostics name the source file and line of each applied Host block and of
// each matched Match block's criteria, also when an earlier Host * masks their values; for each, it takes the source OpenSSH
// read, the configuration bytes already loaded or an included file that readSSHSource reads within sshSourceLimit, and
// inspects only that block's header and the option keywords of its own body. A Host block is global when its patterns include
// one that matches every allowed alias, such as *, "*", or ?*, and no negated pattern, so Host * lab is global and
// Host * !foo is specific to every host it matches. A Match block is specific when a host or originalhost criterion, in
// any letter case, is negated or narrower than every allowed alias, so Match host * and Match all are global; quoted
// arguments, such as an exec command, stay single arguments. A specific Host block whose matching pattern has no wildcard,
// such as Host lab, is a hand-written entry for the alias and claims it by setting any option, Port or Include included;
// an empty one claims nothing. A narrower Host glob, such as Host lab-*, or a specific Match block claims the alias only
// by setting an entry option, even to the intended value or OpenSSH's default, so such a block that sets only Port claims
// nothing. Global settings are left to checkSSHEntry. When a specific block's source is not named, cannot be read, holds
// more than sshSourceLimit bytes, or has no such header at the reported line, or the body of a glob or Match block
// includes another file, it falls back to sshOptionsSetForAlias; a literal Host block whose source cannot be inspected
// this way still claims the alias and, when nothing else names the claim, is reported by its file and line.
func sshAliasClaimed(root *os.Root, config string, content []byte, alias string, effects Effects) ([]string, error) {
	_, diagnostics, err := runSSHQuery([]string{"-vvv", "-G", "-F", config, alias}, nil, alias, effects)
	if err != nil {
		return nil, err
	}
	claimedOptions := map[string]bool{}
	var otherKeywords, unreadLiteralBlocks []string
	needsProbe := false
	for _, stanza := range sshAppliedStanzas(diagnostics) {
		header, keywords, known := "", []string(nil), false
		if lines, ok := sshStanzaSourceLines(root, config, content, stanza.file); ok {
			header, keywords, known = sshStanzaClaims(lines, stanza.line, stanza.match)
		}
		if !stanza.specific(header) {
			continue
		}
		if !known {
			needsProbe = true
			if stanza.literal() {
				unreadLiteralBlocks = append(unreadLiteralBlocks, fmt.Sprintf("options at %s line %d", stanza.file, stanza.line))
			}
			continue
		}
		for _, keyword := range keywords {
			if index := slices.IndexFunc(sshEntryOptions, func(option sshEntryOption) bool { return strings.EqualFold(keyword, option.keyword) }); index >= 0 {
				claimedOptions[sshEntryOptions[index].keyword] = true
			} else if stanza.literal() {
				otherKeywords = appendSSHKeywordOnce(otherKeywords, keyword)
			} else if strings.EqualFold(keyword, "Include") {
				needsProbe = true
			}
		}
	}
	if needsProbe {
		probed, err := sshOptionsSetForAlias(content, alias, effects)
		if err != nil {
			return nil, err
		}
		for _, keyword := range probed {
			claimedOptions[keyword] = true
		}
	}
	if claimed := append(sshKeywordsInEntryOrder(claimedOptions), otherKeywords...); len(claimed) > 0 {
		return claimed, nil
	}
	return unreadLiteralBlocks, nil
}

// sshKeywordsInEntryOrder returns, in entry order, the keyword of each entry option whose keyword set holds.
func sshKeywordsInEntryOrder(set map[string]bool) []string {
	var keywords []string
	for _, option := range sshEntryOptions {
		if set[option.keyword] {
			keywords = append(keywords, option.keyword)
		}
	}
	return keywords
}

// appendSSHKeywordOnce returns keywords with keyword appended, unless keywords already holds it in any letter case.
func appendSSHKeywordOnce(keywords []string, keyword string) []string {
	if slices.ContainsFunc(keywords, func(seen string) bool { return strings.EqualFold(seen, keyword) }) {
		return keywords
	}
	return append(keywords, keyword)
}

// sshAppliedStanza is one Host or Match block that OpenSSH's diagnostics report as applied to a host: the file and line
// of its header when they are named, whether it is a Match block, and the pattern or criteria OpenSSH echoes for it.
type sshAppliedStanza struct {
	file   string
	line   int
	match  bool
	echoed string
}

// specific reports whether the stanza is specific to the host rather than global. A Host block is judged by the patterns
// of its source header when one is given, and otherwise by the one pattern OpenSSH echoes, which omits the others and
// any negation; a Match block is judged by the criteria OpenSSH echoes.
func (s sshAppliedStanza) specific(header string) bool {
	if s.match {
		return sshMatchSpecific(s.echoed)
	}
	patterns := []string{s.echoed}
	if header != "" {
		patterns = sshHeaderArguments(header)
	}
	return !sshPatternsGlobal(patterns)
}

// literal reports whether the stanza is a Host block whose matching pattern, the first positive pattern OpenSSH echoes as
// matched, has no * or ? wildcard, so the pattern names the alias itself as a hand-written entry for it does.
func (s sshAppliedStanza) literal() bool {
	return !s.match && s.echoed != "" && !strings.ContainsAny(s.echoed, "*?")
}

// sshAppliedStanzas lists, in order, the Host blocks and matched Match blocks that OpenSSH's diagnostics report as applied.
// A Host block carries the file and line from its debug1 line; a Match block carries those of its first debug3 criterion
// line, which is its header, and none for criteria such as all that OpenSSH checks without one or at lower verbosity.
func sshAppliedStanzas(diagnostics string) []sshAppliedStanza {
	var applied []sshAppliedStanza
	var pending *sshAppliedStanza
	for _, line := range strings.Split(diagnostics, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "debug1: "); ok {
			if location, pattern, ok := strings.Cut(rest, ": Applying options for "); ok {
				file, number := sshDiagnosticLocation(location)
				applied = append(applied, sshAppliedStanza{file: file, line: number, echoed: pattern})
			}
			continue
		}
		if rest, ok := strings.CutPrefix(line, "debug2: checking match for '"); ok {
			criteria, _, _ := strings.Cut(rest, "' host ")
			pending = &sshAppliedStanza{match: true, echoed: criteria}
			continue
		}
		if rest, ok := strings.CutPrefix(line, "debug3: "); ok && pending != nil && pending.file == "" {
			for _, marker := range []string{": matched '", ": not matched '"} {
				if location, _, ok := strings.Cut(rest, marker); ok {
					pending.file, pending.line = sshDiagnosticLocation(location)
					break
				}
			}
			continue
		}
		if line == "debug2: match found" && pending != nil {
			applied = append(applied, *pending)
		}
		if line == "debug2: match found" || line == "debug2: match not found" {
			pending = nil
		}
	}
	return applied
}

// sshDiagnosticLocation splits an OpenSSH diagnostic location of the form "FILE line N" into the file and line number,
// or returns no file when the location does not have that form.
func sshDiagnosticLocation(location string) (string, int) {
	at := strings.LastIndex(location, " line ")
	if at < 0 {
		return "", 0
	}
	number, err := strconv.Atoi(location[at+len(" line "):])
	if err != nil || number < 1 {
		return "", 0
	}
	return location[:at], number
}

// sshStanzaSourceLines returns the lines of the source file holding an applied stanza, at the path OpenSSH reports: the
// bytes already loaded under the lock for the configuration file itself, or another source that readSSHSource reads. It
// returns false when OpenSSH names no file, or when that other source cannot be read or holds more than sshSourceLimit
// bytes.
func sshStanzaSourceLines(root *os.Root, config string, content []byte, file string) ([]string, bool) {
	if file == "" {
		return nil, false
	}
	data := content
	if file != config {
		var err error
		if data, err = readSSHSource(root, file); err != nil {
			return nil, false
		}
	}
	return strings.Split(string(data), "\n"), true
}

// readSSHSource reads a configuration source that OpenSSH reported reading as agent, opening it read-only through root
// when it lies inside the agent's home and directly when it is another absolute path. It reads at most one byte beyond
// sshSourceLimit and refuses a source longer than that limit, a relative path, or one it cannot open or read.
func readSSHSource(root *os.Root, path string) ([]byte, error) {
	var file *os.File
	var err error
	if relative, relErr := filepath.Rel(root.Name(), path); relErr == nil && filepath.IsLocal(relative) {
		file, err = root.Open(relative)
	} else if filepath.IsAbs(path) {
		file, err = os.Open(path)
	} else {
		return nil, fmt.Errorf("SSH configuration source %s is not an absolute path", path)
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, sshSourceLimit+1))
	if err == nil && len(data) > sshSourceLimit {
		err = fmt.Errorf("SSH configuration source %s exceeds %d bytes", path, sshSourceLimit)
	}
	return data, err
}

// sshStanzaClaims returns the header of the Host or Match block at the 1-based line number and, once each in the order
// and letter case first written, every keyword its own body sets up to the next Host or Match line, Include among them;
// known is false when that line is not such a header.
func sshStanzaClaims(lines []string, number int, match bool) (header string, keywords []string, known bool) {
	kind := "Host"
	if match {
		kind = "Match"
	}
	if number < 1 || number > len(lines) || !strings.EqualFold(sshConfigKeyword(lines[number-1]), kind) {
		return "", nil, false
	}
	for _, line := range lines[number:] {
		keyword := sshConfigKeyword(line)
		if strings.EqualFold(keyword, "Host") || strings.EqualFold(keyword, "Match") {
			break
		}
		if keyword != "" {
			keywords = appendSSHKeywordOnce(keywords, keyword)
		}
	}
	return strings.TrimSpace(lines[number-1]), keywords, true
}

// sshConfigKeyword returns the keyword of one ssh_config line, up to its first space, tab, or equals sign, or nothing for
// a blank or comment line.
func sshConfigKeyword(line string) string {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return ""
	}
	if end := strings.IndexAny(line, " \t="); end >= 0 {
		return line[:end]
	}
	return line
}

// sshHeaderArguments returns the arguments of an ssh_config header line after its keyword and an optional equals sign,
// such as the patterns of a Host line, split as sshConfigArguments does.
func sshHeaderArguments(header string) []string {
	rest := strings.TrimSpace(strings.TrimSpace(header)[len(sshConfigKeyword(header)):])
	return sshConfigArguments(strings.TrimPrefix(rest, "="))
}

// sshConfigArguments splits ssh_config arguments at unquoted spaces and tabs, as OpenSSH does, keeping a double-quoted
// span, such as a Match exec command or a quoted Host pattern, inside one argument without its quotes; a backslash before
// a double quote inside a quoted span keeps the quote. It is no shell parser and leaves every other character as written.
func sshConfigArguments(text string) []string {
	var arguments []string
	var current strings.Builder
	quoted, started := false, false
	for i := 0; i < len(text); i++ {
		switch c := text[i]; {
		case quoted && c == '\\' && i+1 < len(text) && text[i+1] == '"':
			current.WriteByte('"')
			i++
		case c == '"':
			quoted, started = !quoted, true
		case !quoted && (c == ' ' || c == '\t'):
			if started {
				arguments = append(arguments, current.String())
				current.Reset()
				started = false
			}
		default:
			current.WriteByte(c)
			started = true
		}
	}
	if started {
		arguments = append(arguments, current.String())
	}
	return arguments
}

// sshUniversalPattern reports whether an OpenSSH host pattern matches every name sshAliasName accepts: it holds at least
// one * and otherwise at most one ?, because every alias has at least one character.
func sshUniversalPattern(pattern string) bool {
	return strings.Contains(pattern, "*") && strings.Trim(pattern, "*?") == "" && strings.Count(pattern, "?") <= 1
}

// sshPatternsGlobal reports whether a Host or Match host pattern list applies to every allowed alias: it holds a universal
// positive pattern and no negated pattern that could exclude one.
func sshPatternsGlobal(patterns []string) bool {
	universal := false
	for _, pattern := range patterns {
		if strings.HasPrefix(pattern, "!") {
			return false
		}
		universal = universal || sshUniversalPattern(pattern)
	}
	return universal
}

// sshMatchSpecific reports whether Match criteria narrow the hosts they match: a host or originalhost criterion, in any
// letter case, that is negated or whose comma-separated pattern list is not global. Criteria are split as
// sshConfigArguments does, so a quoted exec command stays one argument; all, canonical, and final take no argument, and
// criteria about users or commands do not make a block specific to an alias.
func sshMatchSpecific(criteria string) bool {
	words := sshConfigArguments(criteria)
	for i := 0; i < len(words); i++ {
		keyword, negated := strings.CutPrefix(words[i], "!")
		if strings.EqualFold(keyword, "all") || strings.EqualFold(keyword, "canonical") || strings.EqualFold(keyword, "final") {
			continue
		}
		argument := ""
		if i+1 < len(words) {
			i++
			argument = words[i]
		}
		if (strings.EqualFold(keyword, "host") || strings.EqualFold(keyword, "originalhost")) && (negated || !sshPatternsGlobal(strings.Split(argument, ","))) {
			return true
		}
	}
	return false
}

// sshOptionsSetForAlias returns, in entry order, the keywords of the entry options that the configuration sets for the
// alias but not equally for every alias; sshAliasClaimed falls back to it for specific blocks it cannot read. It has
// ssh -G resolve the configuration followed by a trailing Host * block of placeholder values, twice with opposite
// IdentitiesOnly values, for the alias and for sshControlAlias. Because OpenSSH keeps the first value it reads, and
// lists an earlier IdentityFile ahead of the placeholder one, an option the alias does not resolve to exactly its
// placeholder was set by an earlier rule; when the control alias, which no specific block matches, resolves the same
// value, a global rule set it. A specific block that sets the same value as an earlier global one cannot be told apart
// from it this way and counts as global. When the -vv diagnostics show a specific block that also matches the control
// alias, the comparison is not used.
func sshOptionsSetForAlias(content []byte, alias string, effects Effects) ([]string, error) {
	set := map[string]bool{}
	for _, probe := range []struct{ name, identitiesOnly string }{{"a", "yes"}, {"b", "no"}} {
		placeholder := sshProbePlaceholder + probe.name
		placeholders := sshEntryValues(placeholder+".invalid", placeholder, "/nonexistent/"+placeholder, probe.identitiesOnly, "/nonexistent/"+placeholder)
		lines, err := sshOptionLines(placeholders)
		if err != nil {
			return nil, err
		}
		probed := appendSSHEntry(content, "Host *\n"+lines)
		resolved, _, err := resolveSSHCandidate(probed, alias, false, effects)
		if err != nil {
			return nil, err
		}
		control, diagnostics, err := resolveSSHCandidate(probed, sshControlAlias, true, effects)
		if err != nil {
			return nil, err
		}
		controlGlobal := !sshSpecificStanza(diagnostics)
		for _, option := range sshEntryOptions {
			got := resolved[option.key]
			if slices.Equal(got, []string{placeholders[option.key]}) || (controlGlobal && slices.Equal(got, control[option.key])) {
				continue
			}
			set[option.keyword] = true
		}
	}
	return sshKeywordsInEntryOrder(set), nil
}

// sshSpecificStanza reports whether OpenSSH's diagnostics show an applied block specific to the host, judged only by what
// OpenSSH echoes: a Host pattern that does not match every allowed alias, or Match criteria that sshMatchSpecific narrows.
func sshSpecificStanza(diagnostics string) bool {
	return slices.ContainsFunc(sshAppliedStanzas(diagnostics), func(stanza sshAppliedStanza) bool { return stanza.specific("") })
}

// checkSSHEntry has ssh -G resolve the alias from the exact configuration bytes about to be written, read from the query's
// standard input, and refuses them when an earlier option in that candidate configuration that matches the alias, such as
// one under Host *, before the first Host or Match line, or in a Match block without host criteria, would make the alias
// resolve another value for an option the entry sets: OpenSSH keeps the first value it reads for most options, and offers
// every IdentityFile that applies, so an earlier IdentityFile adds a key beside the dedicated one. The error names the
// alias, each such option with its resolved and intended values, and that reason, and asks for the earlier setting to be
// changed instead of offering another question; whether that setting also matches other aliases depends on its own rule.
func checkSSHEntry(content []byte, alias string, want map[string]string, effects Effects) error {
	resolved, _, err := resolveSSHCandidate(content, alias, false, effects)
	if err != nil {
		return err
	}
	var overridden []string
	for _, option := range sshEntryOptions {
		if got := resolved[option.key]; !slices.Equal(got, []string{want[option.key]}) {
			reason := "it keeps the first value it reads"
			if option.collects {
				reason = "it offers every IdentityFile that applies"
			}
			overridden = append(overridden, fmt.Sprintf("%s %q instead of %q because %s", option.key, strings.Join(got, ", "), want[option.key], reason))
		}
	}
	if len(overridden) > 0 {
		return fmt.Errorf("an earlier setting in the agent's SSH configuration would still apply for alias %s, which OpenSSH would resolve with %s; change that setting and retry; SSH configuration unchanged", alias, strings.Join(overridden, "; "))
	}
	return nil
}

// resolveSSHCandidate has ssh -G resolve the host from configuration bytes read from the query's standard input and returns
// each option's values by ssh -G key, with OpenSSH's -vv diagnostics when verbose is set.
func resolveSSHCandidate(content []byte, host string, verbose bool, effects Effects) (map[string][]string, string, error) {
	args := []string{"-G", "-F", sshCandidateConfig, host}
	if verbose {
		args = append([]string{"-vv"}, args...)
	}
	output, diagnostics, err := runSSHQuery(args, bytes.NewReader(content), host, effects)
	if err != nil {
		return nil, "", err
	}
	resolved := map[string][]string{}
	for _, line := range strings.Split(output, "\n") {
		if key, value, ok := strings.Cut(line, " "); ok {
			resolved[key] = append(resolved[key], value)
		}
	}
	return resolved, diagnostics, nil
}

// runSSHQuery runs one ssh query for queriedAlias, the alias or the control alias, as agent with the fixed SSH environment
// and returns its output and diagnostics, refusing output beyond sshQueryLimit or diagnostics beyond sshDiagnosticLimit.
// A failure names queriedAlias and reports OpenSSH's own diagnostic lines without the debug lines that verbose queries add.
func runSSHQuery(args []string, stdin io.Reader, queriedAlias string, effects Effects) (string, string, error) {
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	stdout, diagnostic := sshOutputBuffer{limit: sshQueryLimit}, sshOutputBuffer{limit: sshDiagnosticLimit}
	err := effects.Start(Process{Program: sshProgram, Args: args, Identity: agentIdentity, EnvironmentPolicy: SSHEnvironment, Stdin: stdin, Stdout: &stdout, Stderr: &diagnostic})
	if err != nil {
		var reported []string
		for _, line := range strings.Split(string(diagnostic.data), "\n") {
			if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "debug") && !strings.HasPrefix(line, "OpenSSH_") {
				reported = append(reported, line)
			}
		}
		return "", "", fmt.Errorf("SSH configuration query for %s failed: %s", queriedAlias, strings.Join(reported, "; "))
	}
	if stdout.overflow || diagnostic.overflow {
		return "", "", fmt.Errorf("SSH configuration query for %s returned too much output", queriedAlias)
	}
	return string(stdout.data), string(diagnostic.data), nil
}
