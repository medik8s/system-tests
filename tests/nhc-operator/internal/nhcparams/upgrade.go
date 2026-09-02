package nhcparams

import "time"

const (
	// UpgradeSubName is the Subscription name used in the NHC upgrade test.
	UpgradeSubName = "nhc-upgrade-sub"

	// NHCUpgradeTestName is the NHC CR name used at the remediation checkpoints
	// of the upgrade test.
	NHCUpgradeTestName = "nhc-upgrade-test"

	// UpgradeRemediationCompletionTimeout is the remediation-completion timeout
	// used at the upgrade test's checkpoints.
	UpgradeRemediationCompletionTimeout = 20 * time.Minute
)
