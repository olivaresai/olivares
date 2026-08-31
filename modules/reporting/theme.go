// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package reporting

// defaultCSS is the professional report theme. Inline CSS for PDF compatibility.
const defaultCSS = `
* { margin: 0; padding: 0; box-sizing: border-box; }
body {
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
  font-size: 11pt; line-height: 1.5; color: #1a1a2e;
  padding: 40px 50px; max-width: 900px; margin: 0 auto;
}
h1 { font-size: 22pt; font-weight: 700; color: #0f0f23; margin-bottom: 4px; }
h2 { font-size: 15pt; font-weight: 600; color: #16213e; margin-top: 28px; margin-bottom: 10px;
     border-bottom: 2px solid #e2e8f0; padding-bottom: 6px; }
h3 { font-size: 12pt; font-weight: 600; color: #1a1a2e; margin-top: 18px; margin-bottom: 6px; }
.subtitle { font-size: 10pt; color: #64748b; margin-bottom: 20px; }
.meta { font-size: 9pt; color: #94a3b8; margin-bottom: 24px; }
.disclaimer { font-size: 8pt; color: #94a3b8; font-style: italic;
              border-top: 1px solid #e2e8f0; padding-top: 12px; margin-top: 30px; }

table { width: 100%; border-collapse: collapse; margin: 12px 0 20px 0; font-size: 10pt; }
th { background: #f1f5f9; color: #334155; font-weight: 600; text-align: left;
     padding: 8px 12px; border-bottom: 2px solid #cbd5e1; }
td { padding: 7px 12px; border-bottom: 1px solid #e2e8f0; vertical-align: top; }
tr:nth-child(even) td { background: #f8fafc; }

.badge { display: inline-block; padding: 2px 8px; border-radius: 4px;
         font-size: 9pt; font-weight: 600; text-transform: uppercase; }
.badge-satisfied { background: #dcfce7; color: #166534; }
.badge-by-design { background: #dbeafe; color: #1e40af; }
.badge-partial   { background: #fef3c7; color: #92400e; }
.badge-gap       { background: #fee2e2; color: #991b1b; }
.badge-unmapped  { background: #f1f5f9; color: #64748b; }

.badge-ok   { background: #dcfce7; color: #166534; }
.badge-warn { background: #fef3c7; color: #92400e; }
.badge-fail { background: #fee2e2; color: #991b1b; }

.stat-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
             gap: 12px; margin: 16px 0; }
.stat-card { background: #f8fafc; border: 1px solid #e2e8f0; border-radius: 8px;
             padding: 14px 16px; text-align: center; }
.stat-value { font-size: 22pt; font-weight: 700; color: #0f0f23; }
.stat-label { font-size: 9pt; color: #64748b; text-transform: uppercase; letter-spacing: 0.5px; }

.progress-bar { background: #e2e8f0; border-radius: 4px; height: 8px; overflow: hidden; margin-top: 4px; }
.progress-fill { height: 100%; border-radius: 4px; }
.progress-ok   { background: #22c55e; }
.progress-warn { background: #f59e0b; }
.progress-over { background: #ef4444; }

.header { display: flex; justify-content: space-between; align-items: flex-start;
          margin-bottom: 24px; border-bottom: 3px solid #0f0f23; padding-bottom: 16px; }
.header-logo { max-height: 48px; }
.footer { font-size: 8pt; color: #94a3b8; text-align: center;
          border-top: 1px solid #e2e8f0; padding-top: 10px; margin-top: 30px; }

@media print {
  body { padding: 20px 30px; }
  .page-break { page-break-before: always; }
}
`

// statusBadgeClass returns the CSS class for a control status.
func statusBadgeClass(status string) string {
	switch status {
	case "satisfied":
		return "badge-satisfied"
	case "by_design":
		return "badge-by-design"
	case "partial":
		return "badge-partial"
	case "gap":
		return "badge-gap"
	case "unmapped":
		return "badge-unmapped"
	default:
		return "badge-unmapped"
	}
}

// budgetBarClass returns the CSS class for a budget progress bar.
func budgetBarClass(pct int, over bool) string {
	if over {
		return "progress-over"
	}
	if pct > 80 {
		return "progress-warn"
	}
	return "progress-ok"
}
