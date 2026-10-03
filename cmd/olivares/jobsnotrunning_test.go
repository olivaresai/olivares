// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
)

func TestEditionJobNotRunningReadsCurrentReason(t *testing.T) {
	collector := &jobsNotRunning{}
	reason := "addon_requires_license"
	if err := collector.register(api.JobDirectorySynchronization, func() string { return reason }); err != nil {
		t.Fatal(err)
	}
	collector.coverageJob(false, api.JobRetention, slog.New(slog.NewTextHandler(io.Discard, nil)))
	assert := func(want []api.JobNotRunning) {
		t.Helper()
		got := collector.list()
		if len(got) != len(want) {
			t.Fatalf("jobs=%v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("jobs=%v, want %v", got, want)
			}
		}
		if len(got) > 0 {
			got[0].Reason = "caller mutation"
		}
	}
	assert([]api.JobNotRunning{{Job: api.JobRetention, Reason: api.JobReasonNoTenantInventory}, {Job: api.JobDirectorySynchronization, Reason: "addon_requires_license"}})
	reason = ""
	assert([]api.JobNotRunning{{Job: api.JobRetention, Reason: api.JobReasonNoTenantInventory}})
	reason = "directory_unavailable"
	assert([]api.JobNotRunning{{Job: api.JobRetention, Reason: api.JobReasonNoTenantInventory}, {Job: api.JobDirectorySynchronization, Reason: "directory_unavailable"}})
}

func TestEditionJobNotRunningRejectsMissingAndDuplicateReaders(t *testing.T) {
	collector := &jobsNotRunning{}
	for _, tc := range []struct {
		job  string
		read func() string
	}{{"", func() string { return "reason" }}, {api.JobDirectorySynchronization, nil}} {
		if err := collector.register(tc.job, tc.read); err == nil {
			t.Fatal("invalid job reader accepted")
		}
	}
	if err := collector.register(api.JobDirectorySynchronization, func() string { return api.JobReasonAddonRequiresLicense }); err != nil {
		t.Fatal(err)
	}
	if err := collector.register(api.JobDirectorySynchronization, func() string { return api.JobReasonDirectoryUnavailable }); err == nil {
		t.Fatal("duplicate reader replaced the owner")
	}
	if got := collector.list(); len(got) != 1 || got[0].Reason != api.JobReasonAddonRequiresLicense {
		t.Fatal("duplicate changed reported status", got)
	}
	collector.coverageJob(false, api.JobRetention, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := collector.register(api.JobRetention, func() string { return api.JobReasonDirectoryUnavailable }); err == nil {
		t.Fatal("coverage job replaced by edition reader")
	}
}

func TestEditionJobNotRunningInvokesReadersOutsideCollectorLock(t *testing.T) {
	collector := &jobsNotRunning{}
	calls := 0
	if err := collector.register(api.JobLegalHoldArchive, func() string {
		calls++
		if calls == 1 {
			if err := collector.register(api.JobAuditCheckpoints, func() string { return api.JobReasonNoTenantInventory }); err != nil {
				t.Error(err)
			}
		}
		return api.JobReasonNoTenantInventory
	}); err != nil {
		t.Fatal(err)
	}
	if got := collector.list(); len(got) != 1 {
		t.Fatal("reader registration changed the current snapshot", got)
	}
	if got := collector.list(); len(got) != 2 {
		t.Fatal("next snapshot omitted registered reader", got)
	}
}

func TestEditionJobNotRunningPreservesLaterCoverageRefusal(t *testing.T) {
	collector := &jobsNotRunning{}
	if err := collector.register(api.JobRetention, func() string { return "" }); err != nil {
		t.Fatal(err)
	}
	collector.coverageJob(false, api.JobRetention, slog.New(slog.NewTextHandler(io.Discard, nil)))
	got := collector.list()
	if len(got) != 1 || got[0].Reason != api.JobReasonNoTenantInventory {
		t.Fatal("edition status hid proved coverage refusal", got)
	}
}

func TestEditionJobNotRunningDropsUnknownReasons(t *testing.T) {
	var log bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&log, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	collector := &jobsNotRunning{}
	reason := "unrecognized-private-reason"
	if err := collector.register(api.JobDirectorySynchronization, func() string { return reason }); err != nil {
		t.Fatal(err)
	}
	if got := collector.list(); len(got) != 0 {
		t.Fatalf("reason outside the public enum reached server-info: %v", got)
	}
	if !strings.Contains(log.String(), "unknown reason") || !strings.Contains(log.String(), api.JobDirectorySynchronization) {
		t.Fatal("unknown reader reason was not diagnosed", log.String())
	}
	if strings.Contains(log.String(), reason) {
		t.Fatal("unrecognized reader data leaked into the log")
	}
	reason = api.JobReasonDirectoryUnavailable
	if got := collector.list(); len(got) != 1 || got[0].Reason != reason {
		t.Fatal("valid reason was suppressed after unknown value", got)
	}
}
