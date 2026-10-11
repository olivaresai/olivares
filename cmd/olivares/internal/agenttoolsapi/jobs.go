// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
)

// Installer receipts may occupy 256 KiB. Leave bounded room for the job envelope,
// progress and escaped probe errors without losing a completed install on restart.
const maxJournalBytes = 512 << 10

// save atomically replaces a bounded journal record inside the owned directory.
// Only canonical job IDs reach file names; os.Root also confines existing symlinks.
func (m *Module) save(j *savedJob) error {
	if m.files == nil {
		return errors.New("job journal unavailable")
	}
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	if len(b) > maxJournalBytes {
		return errors.New("job journal record exceeds its bound")
	}
	name := j.ID.String() + ".json"
	tmp := j.ID.String() + "." + model.NewID().String() + ".tmp"
	f, err := m.files.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer m.files.Remove(tmp)
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := m.files.Rename(tmp, name); err != nil {
		return err
	}
	// A file fsync does not retain its new directory entry across power loss.
	// Persist the rename before the caller starts or acknowledges an install.
	directory, err := m.files.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
func (m *Module) recover() error {
	dir, err := m.files.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(maxRetained + 1)
	if err != nil && err != io.EOF {
		return err
	}
	if len(entries) > maxRetained {
		return errors.New("agent tool job journal exceeds its bound")
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		} // Preserve interrupted temp writes as evidence.
		id, err := model.ParseID(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil || id.String()+".json" != entry.Name() {
			return errors.New("invalid job journal entry")
		}
		f, err := m.files.Open(entry.Name())
		if err != nil {
			return err
		}
		info, se := f.Stat()
		if se != nil || !info.Mode().IsRegular() || info.Size() > maxJournalBytes {
			f.Close()
			return errors.New("invalid job journal file")
		}
		b, err := io.ReadAll(io.LimitReader(f, maxJournalBytes+1))
		f.Close()
		if err != nil {
			return err
		}
		if len(b) > maxJournalBytes {
			return errors.New("job journal file grew beyond its bound")
		}
		var j savedJob
		if err = json.Unmarshal(b, &j); err != nil || j.ID != id || j.Tenant.IsZero() {
			return errors.New("invalid job journal record")
		}
		if j.State == "running" {
			j.State = "interrupted"
			j.Error = "The control plane stopped before recording the outcome. Check inventory before retrying."
			j.UpdatedAt = time.Now().UTC()
			if !m.readOnly {
				if err = m.save(&j); err != nil {
					return err
				}
			}
		}
		m.jobs[id] = &j
	}
	return nil
}

type progressWriter struct {
	m *Module
	j *savedJob
}

func (p progressWriter) Write(b []byte) (int, error) {
	p.m.mu.Lock()
	defer p.m.mu.Unlock()
	// Keep the latest progress and disclose journal failures.
	p.j.Progress += string(b)
	if len(p.j.Progress) > 16<<10 {
		p.j.Progress = p.j.Progress[len(p.j.Progress)-(16<<10):]
	}
	p.j.UpdatedAt = time.Now().UTC()
	// Engine logging is advisory: it does not inspect writer errors. Retain the failure visibly.
	if err := p.m.save(p.j); err != nil {
		p.j.Error = "Progress could not be retained in the job journal."
	}
	return len(b), nil
}
func (m *Module) run(j *savedJob, p *Plan, mc api.ModuleContext) {
	defer m.wg.Done()
	ctx, cancel := context.WithTimeout(m.ctx, 20*time.Minute)
	defer cancel()
	var receipt any
	var err error
	out := progressWriter{m, j}
	if p.v1 != nil {
		receipt, _, err = m.engine.Install(ctx, toolinstall.Request{Driver: p.Driver, Version: p.requested, Platform: toolinstall.HostPlatform(), DestRoot: m.root}, p.v1, out)
	} else {
		receipt, _, err = m.engine.InstallV2(ctx, toolinstall.RequestV2{Driver: p.Driver, Version: p.requested, Platform: p.v2.Selection.Platform, DestRoot: m.root}, p.v2, out)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active = false
	for key := range m.statusReads {
		if key.driver == p.Driver {
			delete(m.statusReads, key)
		}
	}
	j.State = "succeeded"
	j.Receipt = receipt
	if err != nil {
		j.State = "failed"
		j.Error = err.Error()
		if errors.Is(err, context.Canceled) {
			j.State = "interrupted"
		}
	}
	j.UpdatedAt = time.Now().UTC()
	auditCtx, done := context.WithTimeout(context.WithoutCancel(m.ctx), 5*time.Second)
	defer done()
	if ae := audit(auditCtx, mc, "install."+j.State, j.ID, map[string]any{"driver": j.Driver, "version": j.Version, "plan_digest": j.Digest}); ae != nil {
		j.AuditError = "The installation outcome could not be appended to the audit ledger."
	}
	if se := m.save(j); se != nil {
		j.Error = "The final installation outcome could not be retained. Check inventory before retrying."
	}
}
