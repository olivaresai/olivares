// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"errors"
	"log/slog"
	"strings"

	"github.com/olivaresai/olivares/core/envconfig"
	"github.com/olivaresai/olivares/modules/sessions"
)

const (
	// envSessionMaxPerUser is how many sessions one person may have running at once.
	envSessionMaxPerUser = "OLIVARES_SESSION_MAX_PER_USER"
	// envSessionMemoryMax and envSessionTasksMax are the ceilings of each session's
	// systemd scope, in the grammar of systemd's MemoryMax= and TasksMax=.
	envSessionMemoryMax = "OLIVARES_SESSION_MEMORY_MAX"
	envSessionTasksMax  = "OLIVARES_SESSION_TASKS_MAX"

	// The defaults leave room for builds and test runs: half of the host's memory is
	// far above any session that works and far below the whole host, and 16384 tasks
	// is far above a build's threads and far below the kernel's limit. A person has no
	// cap by default: a single-user host never feels one.
	defaultSessionMemoryMax = "50%"
	defaultSessionTasksMax  = "16384"
)

// sessionLimitsOption reads the per-person cap and the per-session ceilings. A cap
// that does not read as a number is reported and not applied, never guessed.
func sessionLimitsOption(getenv func(string) string, log *slog.Logger) sessions.Option {
	limits := sessions.SessionLimits{MemoryMax: defaultSessionMemoryMax, TasksMax: defaultSessionTasksMax}
	if v := strings.TrimSpace(getenv(envSessionMemoryMax)); v != "" {
		limits.MemoryMax = v
	}
	if v := strings.TrimSpace(getenv(envSessionTasksMax)); v != "" {
		limits.TasksMax = v
	}
	perUser, err := envconfig.Reader{Source: getenv}.Int(envSessionMaxPerUser, 0)
	if err == nil && perUser < 0 {
		err = errors.New(envSessionMaxPerUser + ": a cap is zero or more")
	}
	if err == nil {
		limits.MaxPerUser = perUser
	}
	if log != nil {
		if err != nil {
			log.Error("session runtime: the per-person session cap is not applied", "error", err.Error())
		}
		log.Info("session runtime: session limits", "max_per_user", limits.MaxPerUser, "memory_max", limits.MemoryMax, "tasks_max", limits.TasksMax)
	}
	return sessions.WithSessionLimits(limits)
}
