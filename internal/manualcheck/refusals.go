package manualcheck

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// nullStdin opens the null device for a command that must see no standard input.
func nullStdin() (*os.File, error) {
	return os.Open(nullInput)
}

// ghStatusProbe prints whether gh reports a GitHub login and its exit status, and nothing else:
// the account name gh prints stays out of the item logs.
const ghStatusProbe = `gh auth status 2>&1 | grep -o 'Logged in to github.com'; echo "gh exit ${PIPESTATUS[0]}"`

// azAccountProbe prints the exit status of az account show and, when an account is selected, the
// SHA-256 of its name in the form the persistence rounds compare; the name, tenant, and
// subscription identifiers stay out of the item logs.
const azAccountProbe = `name=$(az account show --query user.name --output tsv 2>/dev/null); echo "az exit $?"; [ -n "$name" ] && printf '%s' "$name" | sha256sum | cut -d' ' -f1 | sed 's/^/az-account-sha256 /'`

// statusProbes records what gh and az report in a sandbox, for a before/after comparison; the
// output stays in the item directory and holds no token and no account identifier.
func (r *Run) statusProbes(dir, sandbox, tag string) error {
	if _, err := r.ShellStep(dir, tag+"-gh-auth-status", sandbox, ghStatusProbe); err != nil {
		return err
	}
	_, err := r.ShellStep(dir, tag+"-az-account-show", sandbox, azAccountProbe)
	return err
}

// LoginNeedsTerminal runs item `login-needs-terminal`: the three logins with standard input from
// nothing are refused non-zero with the documented message and change neither the state nor the
// earlier sign-ins.
func (r *Run) LoginNeedsTerminal(sandbox string) error {
	if !namePattern.MatchString(sandbox) {
		return fmt.Errorf("invalid sandbox name %q", sandbox)
	}
	dir, err := r.ItemDir("login-needs-terminal")
	if err != nil {
		return err
	}
	failures := &Failures{run: r}
	if err := r.CaptureState(filepath.Join(dir, "before-term.txt"), sandbox); err != nil {
		return err
	}
	if err := r.statusProbes(dir, sandbox, "before"); err != nil {
		return err
	}
	for index, login := range [][]string{{"github"}, {"azure", "device-code"}, {"azure", "browser"}} {
		null, err := nullStdin()
		if err != nil {
			return err
		}
		args := append([]string{"integrations", "login", sandbox}, login...)
		result, err := Step(dir, fmt.Sprintf("login-%d", index+1), null, r.SA(args...))
		null.Close()
		if err != nil {
			return err
		}
		if result.Exit == 0 || !result.Contains("needs an interactive terminal") {
			failures.Addf("integrations login %s %s was not refused for a missing terminal (exit %d)", sandbox, strings.Join(login, " "), result.Exit)
			continue
		}
		r.Printf("refused without a terminal: integrations login %s %s (exit %d)\n", sandbox, strings.Join(login, " "), result.Exit)
	}
	if err := r.CaptureState(filepath.Join(dir, "after-term.txt"), sandbox); err != nil {
		return err
	}
	if err := r.statusProbes(dir, sandbox, "after"); err != nil {
		return err
	}
	if err := CompareFiles(filepath.Join(dir, "before-term.txt"), filepath.Join(dir, "after-term.txt")); err != nil {
		failures.Addf("%v", err)
	}
	for _, probe := range []string{"gh-auth-status", "az-account-show"} {
		if err := CompareFiles(filepath.Join(dir, "before-"+probe+".log"), filepath.Join(dir, "after-"+probe+".log")); err != nil {
			failures.Addf("%s changed: %v", probe, err)
		}
	}
	return failures.Finish(dir, "login-needs-terminal", "three logins refused non-zero for a missing terminal; state and sign-ins unchanged")
}

// AzureToolchainRequired runs item `azure-toolchain-required`: in two sandboxes without the azure
// toolchain, the three Azure logins are refused before anything is stored, naming the toolchain
// and the exact update command, and nothing changes. The sandboxes are removed unless keep is set.
func (r *Run) AzureToolchainRequired(keep bool) error {
	dir, err := r.ItemDir("azure-toolchain-required")
	if err != nil {
		return err
	}
	failures := &Failures{run: r}
	for _, up := range [][]string{{"up", "chk-base"}, {"up", "chk-native", "--with", "native"}} {
		result, err := Step(dir, strings.Join(up[:2], "-"), nil, r.SA(up...))
		if err != nil {
			return err
		}
		if result.Exit != 0 {
			return fmt.Errorf("%s failed with exit %d; see %s", strings.Join(up, " "), result.Exit, result.Log)
		}
	}
	for _, name := range []string{"chk-base", "chk-native"} {
		repair := "update " + name + " --with azure"
		if name == "chk-native" {
			repair = "update chk-native --with azure,native"
		}
		if err := r.CaptureState(filepath.Join(dir, "before-"+name+".txt"), name); err != nil {
			return err
		}
		for index, login := range [][]string{{"azure", "device-code"}, {"azure", "browser"}, {"azdo", "--organization", "https://dev.azure.com/example-organization"}} {
			// The toolchain check precedes the terminal check, so no terminal is needed to observe it.
			null, err := nullStdin()
			if err != nil {
				return err
			}
			args := append([]string{"integrations", "login", name}, login...)
			result, err := Step(dir, fmt.Sprintf("%s-login-%d", name, index+1), null, r.SA(args...))
			null.Close()
			if err != nil {
				return err
			}
			output := result.Output()
			if result.Exit == 0 || !strings.Contains(output, "requires the azure toolchain") || !strings.Contains(output, repair) || strings.Contains(output, "--with none") || strings.Contains(output, ",none") {
				failures.Addf("%s: integrations login %s not refused as documented (exit %d)", name, strings.Join(login, " "), result.Exit)
				continue
			}
			r.Printf("%s: refused, names the azure toolchain and %q: integrations login %s (exit %d)\n", name, repair, strings.Join(login, " "), result.Exit)
		}
		if err := r.CaptureState(filepath.Join(dir, "after-"+name+".txt"), name); err != nil {
			return err
		}
		if err := CompareFiles(filepath.Join(dir, "before-"+name+".txt"), filepath.Join(dir, "after-"+name+".txt")); err != nil {
			failures.Addf("%v", err)
		}
		result, err := r.ShellStep(dir, name+"-credential-config", name, `git config --global --get-regexp '^credential\.'; echo "git exit $?"`)
		if err != nil {
			return err
		}
		if strings.Contains(result.Output(), "credential.") {
			failures.Addf("%s: a credential helper is configured", name)
		}
	}
	if !keep {
		for _, name := range []string{"chk-base", "chk-native"} {
			result, err := Step(dir, "remove-"+name, nil, r.SA("remove", name, "--volumes"))
			if err != nil {
				return err
			}
			if result.Exit != 0 {
				failures.Addf("remove %s exited %d", name, result.Exit)
			}
		}
	}
	return failures.Finish(dir, "azure-toolchain-required", "six logins refused non-zero naming the azure toolchain and the exact update command; state unchanged; no credential helper")
}
