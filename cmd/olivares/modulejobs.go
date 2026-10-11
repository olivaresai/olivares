// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

// C2 item 1 (ARCH, 2026-10-01): one job table owned per module, instead of
// thirteen if-nil-register blocks inline in boot. The gate is structural —
// boot schedules only the entries whose module this node's profile runs — and
// the table is the one place that lists every periodic job the engine has.

// moduleJob is one periodic job of a module: its profile gate (an empty module
// always schedules) and the registration block that used to sit inline in boot.
type moduleJob struct {
	// module is the profile namespace the job belongs to. Empty means a core
	// job that always schedules (the audit archival, and anything else the
	// profile does not turn off).
	module string
	// name is the scheduler's job name, for logs and tests.
	name string
	// schedule performs the constructor's nil-check and registration exactly
	// the way the inline block did, including its warn text.
	schedule func()
}

// scheduleModuleJobs runs each job's registration once, in table order, for the
// modules this node's profile runs. Today that is the same outcome the
// constructors' nil checks already produced (a dormant module built a nil pump
// and nothing registered); the table makes it structural and visible.
func scheduleModuleJobs(profile moduleProfile, jobs []moduleJob) {
	for _, j := range jobs {
		if j.module == "" || profile.Active(j.module) {
			j.schedule()
		}
	}
}

// deployDriftRegistrationEnabled is the item-2 predicate: the drift loop
// registers only when the profile runs deploy on this node.
func deployDriftRegistrationEnabled(profile moduleProfile, d *deployDriftLoop) bool {
	return d != nil && profile.Active("deploy")
}
