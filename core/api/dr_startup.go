// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"fmt"
)

// RunStartupBackup runs one backup through the exact same path the console
// trigger uses (job tracker + runBackup), but synchronously. It is intended for
// explicit startup workflows such as the synthetic demo estate.
func (s *Server) RunStartupBackup(ctx context.Context, passphrase, notes, actor string) error {
	if s.drSvc == nil {
		return fmt.Errorf("startup backup: %w", errDRUnavailable)
	}
	if passphrase == "" {
		return fmt.Errorf("startup backup: passphrase is required")
	}
	// Same floor as the console trigger — this IS the console path, run
	// synchronously (the demo passphrase already satisfies it).
	if msg := drPassphraseFloorError(passphrase); msg != "" {
		return fmt.Errorf("startup backup: %s", msg)
	}
	if err := s.drSvc.ensureBackupDir(); err != nil {
		return fmt.Errorf("startup backup: backup dir: %w", err)
	}

	job := s.drSvc.jobs.create(drJobBackup, notes)
	s.runBackup(ctx, job.ID, passphrase, notes, actor)

	completed, ok := s.drSvc.jobs.get(job.ID)
	if !ok {
		return fmt.Errorf("startup backup: job %s disappeared from tracker", job.ID)
	}
	if completed.Status != drJobCompleted {
		if completed.Error != "" {
			return fmt.Errorf("startup backup: %s", completed.Error)
		}
		return fmt.Errorf("startup backup: job %s ended with status %q", job.ID, completed.Status)
	}
	return nil
}
