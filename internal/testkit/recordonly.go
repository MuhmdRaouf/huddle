package testkit

import (
	"os"
	"runtime"
)

// RecordOnlyNotice is the line a perf guard logs when RecordOnly holds, in place of failing.
const RecordOnlyNotice = "record only on macOS CI until the same-job baseline lands (HV-PERF-GUARD-BASELINE)"

// RecordOnly is true when the test runs in GitHub Actions on macOS, where the shared runner is
// slower than the budgets assume. The perf guards (TestHubIdleCPU, TestE2ERouterOverhead) log
// their numbers there and keep their budgets everywhere else.
//
// HV-PERF-GUARD-BASELINE removes this rule, the guards' record-only branches and
// TestRecordOnlyEndsWithTheBaseline.
func RecordOnly() bool { return RecordOnlyOn(runtime.GOOS) }

// RecordOnlyOn is RecordOnly for a given GOOS.
func RecordOnlyOn(goos string) bool {
	return os.Getenv("GITHUB_ACTIONS") == "true" && goos == "darwin"
}
