package liverun

// CaseOverlapEnv carries the saved independent-case and initial-toolchain concurrency limit from the helper to the live suite.
const CaseOverlapEnv = "SANDBOXED_AGENTS_LIVE_OVERLAP"

// BuildJobsEnv carries the saved worker limit for the live suite's single no-cache rebuild.
const BuildJobsEnv = "SANDBOXED_AGENTS_LIVE_BUILD_JOBS"

// StopAfterFailureEnv makes the helper-started suite stop admitting work after a failure when its value is 1.
const StopAfterFailureEnv = "SANDBOXED_AGENTS_LIVE_STOP_AFTER_FAILURE"
