// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

// Use the earlier of the template's duration and the caller's absolute deadline.
// Recompute at launch: repository fetch, approval and setup consume run time too.
func (m *Module) armPipelineDeadline(lr *liveRun, p CreateRunParams) {
	d := p.MaxDuration
	if !p.runDeadline.IsZero() {
		remaining := p.runDeadline.Sub(m.now())
		if remaining <= 0 {
			m.stopPipelineRun(lr, "run timeout reached")
			return
		}
		if d <= 0 || remaining <= d {
			d = remaining
			lr.runTimeout = true
		}
	}
	m.armRunDeadline(lr, d)
}

// Record the limit before scheduling teardown. A concurrent operator stop drains
// the last usage report through the bridge and must retain its classified reason.
func (m *Module) stopPipelineRun(lr *liveRun, reason string) {
	lr.mu.Lock()
	lr.stopReason = reason
	lr.mu.Unlock()
	go m.stopOwnedRun(lr, reason)
}
