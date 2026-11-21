package liverun

// CaseOverlapEnv selects the live suite's independent-case and initial-toolchain work budget.
const CaseOverlapEnv = "SANDBOXED_AGENTS_LIVE_OVERLAP"

// BuildJobsEnv carries the saved worker limit for the live suite's single no-cache rebuild.
const BuildJobsEnv = "SANDBOXED_AGENTS_LIVE_BUILD_JOBS"

// StopAfterFailureEnv makes the live suite stop admitting work after a failure when its value is 1.
const StopAfterFailureEnv = "SANDBOXED_AGENTS_LIVE_STOP_AFTER_FAILURE"

// OwnedGroupsEnv names the private directory where the Linux live suite records
// each running owned invocation's process group, so its supervisor can end the
// invocations a dying suite leaves running. It is empty on other hosts.
const OwnedGroupsEnv = "SANDBOXED_AGENTS_LIVE_OWNED_GROUPS_DIR"
