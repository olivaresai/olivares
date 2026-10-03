// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/store"
)

// jobsNotRunning collects the background jobs this node composes but cannot run,
// for server-info (api.Options.JobsNotRunning). A job that must cover every
// tenant (retention, legal-hold archive, audit checkpoints and archival) reads
// System.ListOrgs and fails closed where the store cannot enumerate every tenant:
// PostgreSQL with neither the closed tenant inventory nor the BYPASSRLS
// administration role. Such a job stays fail-closed; this makes the product say
// so instead of skipping in the log only.
type jobsNotRunning struct {
	mu      sync.Mutex
	jobs    []api.JobNotRunning
	readers []jobNotRunningReader
	log     *slog.Logger
}

type jobNotRunningReader struct {
	job    string
	reason func() string
}

// register binds one edition job's current reason to the existing status reader.
// The callback must read only bounded in-memory state and return a public reason
// code, or empty when the job runs. Registration grants no scheduling authority.
func (j *jobsNotRunning) register(job string, reason func() string) error {
	if j == nil || job == "" || reason == nil {
		return errors.New("job status reader requires a name and callback")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if slices.ContainsFunc(j.jobs, func(n api.JobNotRunning) bool { return n.Job == job }) || slices.ContainsFunc(j.readers, func(n jobNotRunningReader) bool { return n.job == job }) {
		return errors.New("job status reader already registered")
	}
	j.readers = append(j.readers, jobNotRunningReader{job: job, reason: reason})
	return nil
}

// estateEnumerable reports whether st can enumerate every tenant
// (ListOrgsVisible's authoritative flag). A read error answers true: nothing is
// reported that was not measured.
func estateEnumerable(ctx context.Context, st store.Store, log *slog.Logger) bool {
	enumerable := true
	if err := st.System(ctx, func(sys store.SystemScope) error {
		_, authoritative, err := sys.ListOrgsVisible(ctx)
		enumerable = authoritative
		return err
	}); err != nil {
		log.Warn("jobs: cannot tell whether this store can enumerate every tenant", "err", err)
		return true
	}
	return enumerable
}

// coverageJob records that job is composed on a store that cannot enumerate every
// tenant, and says so once in the log, as a warning: the state is deliberate and has
// a remedy, it is not an engine fault. It does nothing when the store can.
func (j *jobsNotRunning) coverageJob(enumerable bool, job string, log *slog.Logger) {
	if j == nil || enumerable {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if slices.ContainsFunc(j.jobs, func(n api.JobNotRunning) bool { return n.Job == job }) {
		return
	}
	j.jobs = append(j.jobs, api.JobNotRunning{Job: job, Reason: api.JobReasonNoTenantInventory})
	log.Warn("jobs: "+job+" does not run: this PostgreSQL database cannot list every tenant (no tenant inventory and no administration role); install the inventory once (run olivares db init again with a superuser DSN and this data directory) and restart (deploy/postgres/README.md)",
		"job", job, "reason", api.JobReasonNoTenantInventory)
}

// list is api.Options.JobsNotRunning.
func (j *jobsNotRunning) list() []api.JobNotRunning {
	j.mu.Lock()
	jobs, readers := slices.Clone(j.jobs), slices.Clone(j.readers)
	j.mu.Unlock()
	for _, reader := range readers {
		// A proved coverage refusal cannot be hidden by an edition callback.
		if slices.ContainsFunc(jobs, func(n api.JobNotRunning) bool { return n.Job == reader.job }) {
			continue
		}
		reason := reader.reason()
		switch reason {
		case "":
		case api.JobReasonNoTenantInventory, api.JobReasonAddonRequiresLicense, api.JobReasonDirectoryUnavailable:
			jobs = append(jobs, api.JobNotRunning{Job: reader.job, Reason: reason})
		default:
			logger := j.log
			if logger == nil {
				logger = slog.Default()
			}
			logger.Warn("background job status reader returned an unknown reason", "job", reader.job)
		}
	}
	return jobs
}
