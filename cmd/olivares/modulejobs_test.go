// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"testing"
)

// C2 item 1: boot schedules only the job-table entries whose module the
// profile runs. A dormant module's closure never fires; a core job (empty
// module) always does; the zero profile runs everything.
func TestScheduleModuleJobsGatesByProfile(t *testing.T) {
	profile, err := resolveModuleProfile([]string{"eventing"})
	if err != nil {
		t.Fatalf("resolveModuleProfile: %v", err)
	}
	ran := map[string]int{}
	jobs := []moduleJob{
		{"", "core-job", func() { ran["core-job"]++ }},
		{"eventing", "eventing-dispatch", func() { ran["eventing-dispatch"]++ }},
		{"notify", "notify-dispatch", func() { ran["notify-dispatch"]++ }},
		{"orchestration", "orchestration-cadence", func() { ran["orchestration-cadence"]++ }},
	}
	scheduleModuleJobs(profile, jobs)
	for _, name := range []string{"core-job", "eventing-dispatch"} {
		if ran[name] != 1 {
			t.Fatalf("%s ran %d times under a profile that runs it, want 1", name, ran[name])
		}
	}
	for _, name := range []string{"notify-dispatch", "orchestration-cadence"} {
		if ran[name] != 0 {
			t.Fatalf("%s ran under a profile that does not select its module", name)
		}
	}
}

func TestScheduleModuleJobsZeroProfileRunsEverything(t *testing.T) {
	ran := 0
	scheduleModuleJobs(moduleProfile{}, []moduleJob{
		{"", "core-job", func() { ran++ }},
		{"eventing", "eventing-dispatch", func() { ran++ }},
		{"notify", "notify-dispatch", func() { ran++ }},
	})
	if ran != 3 {
		t.Fatalf("the zero profile must run every entry, ran %d of 3", ran)
	}
}

// --- C2 items 2-6 predicates and throttles -----------------------------------

func TestDeployDriftRegistrationEnabledBothWays(t *testing.T) {
	withDeploy, err := resolveModuleProfile([]string{"deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if !deployDriftRegistrationEnabled(withDeploy, &deployDriftLoop{}) {
		t.Fatal("a profile running deploy must register the drift loop")
	}
	withoutDeploy, err := resolveModuleProfile([]string{"eventing"})
	if err != nil {
		t.Fatal(err)
	}
	if deployDriftRegistrationEnabled(withoutDeploy, &deployDriftLoop{}) {
		t.Fatal("a profile without deploy must not register the drift loop")
	}
	if deployDriftRegistrationEnabled(withDeploy, nil) {
		t.Fatal("nil loop never registers")
	}
}
