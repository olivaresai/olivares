<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# WCAG 2.2 AA — Admin panel conformance checklist

**Scope:** the embedded operations console (`/web`, served by `cmd/olivares`).
**Standard:** [WCAG 2.2](https://www.w3.org/TR/WCAG22/) Level AA (current W3C Recommendation; SC 4.1.1 Parsing was removed in 2.2 and is not tested).
**Method:** static review + axe-core (`wcag2a wcag2aa wcag21a wcag21aa wcag22aa` + best-practice) over enumerated route inventories in **both** themes + manual keyboard/APG review + a **platform-accessibility-tree verification harness** (`web/e2e-visual/at-run.ts`: programmatic role/name/state, heading/landmark structure, live-region coverage, and contrast over pairings **derived from the console's own source** — `at-pairs.ts`, **1,196 real compositions per theme**, one row per site — plus the **47-pairing design-token contract**). `registry.a11y-coverage.test.ts` fails if any of the **54 registered authenticated feature views** (`FEATURE_VIEWS`, `web/src/features/registry.tsx`) is absent from those inventories, so a new view cannot silently escape scanning. The gate walks **59 routes**: `AUTH_ROUTES` (55 = the 54 registered views + the harness-only `/settings`) plus the 4 public forms/pages (`/login`, `/setup`, `/accept-invite`, `/status-page`). **Counted from the source inventories on 2026-08-22.** This file is the durable baseline later admin views must not regress; the formal procurement artefact is [`VPAT-olivares-admin.md`](./VPAT-olivares-admin.md).

> Honesty note: this checklist asserts only what has been verified in code/review/axe **and the platform accessibility API** (the surface NVDA/JAWS/VoiceOver consume). The Rev 1.2 automated AT pass over its 33-view surface and the later Rev 1.4/1.5 passes over their recorded surfaces are complete; a numeric re-issue over the current **54-view** registered surface is pending a clean expanded pass. The one method limit is that a **human** screen-reader walkthrough was not run (the holistic Functional-Performance rows 302.1/4.2.1 in the VPAT carry a residual human-spot-test recommendation). Items needing views not yet built are marked **Partial** with the reason, never overstated.

## New-in-2.2 success criteria

| SC | Level | Status | Evidence |
|---|---|---|---|
| **2.4.11 Focus Not Obscured (Minimum)** | AA | **Supports** | The shell scroll region is `<main>` with the topbar as a non-overlapping sibling (no sticky chrome covers it). The one sticky surface — the `DataTable` header — reserves its height with `scroll-pt-10` on the scroll container so a focused cell scrolled to the top is not hidden (CSS technique C43). `web/src/components/data/data-table.tsx`. |
| **2.5.7 Dragging Movements** | AA | **Supports** | The access graph is the only drag surface. Nodes are non-draggable (`nodesDraggable=false`). Pan/zoom have single-pointer non-drag alternatives: the React Flow `Controls` zoom-in/zoom-out/fit-view buttons (`web/src/features/shared/graph/graph-canvas.tsx`) and, at scale, the WebGL view's explicit zoom **+ / −**, **reset** and pan buttons. `DataTable` has no drag-to-reorder. |
| **2.5.8 Target Size (Minimum)** | AA | **Supports** | All design-system controls are ≥ 24×24 CSS px. Button sizes: `sm`/`icon-sm` = 28px, `base`/`icon` = 32px. Checkbox (visual 16px) and Switch (visual 20px tall) keep their look but extend the pointer target to ≥24px via a transparent `::before` that belongs to the control (`checkbox.tsx`, `switch.tsx`). DataTable sort buttons `min-h-6`; graph control buttons `min-h-6 min-w-6`; treegrid twistie `size-6`. |
| **3.2.6 Consistent Help** | A | **Supports** | The authenticated shell exposes one consistent self-help/navigation mechanism — the ⌘K command palette — in the same topbar position on every page (rendered by the shared `Topbar`, so order cannot drift). `web/src/components/layout/topbar.tsx` + `command-menu.tsx`. The pre-auth login/setup pages carry no help mechanism, so 3.2.6 is trivially met there (it applies only when a help mechanism is present). |
| **3.3.7 Redundant Entry** | A | **Supports** | Login and setup are independent single-step forms; neither re-asks for information already entered in the same process (no confirm-password re-entry, no multi-step wizard). `web/src/app/pages/login.tsx`, `setup.tsx`. |
| **3.3.8 Accessible Authentication (Minimum)** | AA | **Supports** | No cognitive-function test beyond a password, and the password "mechanism" exception is satisfied: secret fields are plain `<input>` with correct `autocomplete` (`current-password` / `new-password` / `username`), no paste blocking, no CAPTCHA/puzzle. The setup token is a pasteable value, not a memorise-and-retype test. `login.tsx`, `setup.tsx`. |

## Supporting remediations raised to the same bar (beyond the named SC)

| SC | Level | Status | Evidence |
|---|---|---|---|
| **2.4.1 Bypass Blocks** | A | **Supports** | A skip link ("Skip to content") is the first focusable element and targets `#main-content` (`app-layout.tsx`). |
| **1.3.1 / 4.1.2 — grid/treegrid semantics** | A | **Supports** | `DataTable` is a `role="grid"` with `aria-rowcount`/`aria-rowindex`/`aria-colcount`/`aria-colindex` (true totals under virtualization) and roving-tabindex arrow navigation; the `TreeGrid` primitive implements the APG `treegrid` pattern (`aria-level`, `aria-expanded`, expand/collapse keys). |
| **4.1.2 — current location** | A | **Supports** | The active sidebar item sets `aria-current="page"` (`sidebar.tsx`). |
| **1.3.1 — landmarks** | A | **Supports** | `<main>` (id'd), `<header>` banner, `<aside>` labelled "Primary", sidebar `<nav>` labelled "Main navigation", breadcrumb `<nav>` labelled. |
| **2.4.3 Focus Order / 2.1.2 No Keyboard Trap** | A | **Supports** | Radix dialog/sheet/popover trap focus while open, Escape closes, and focus RETURNS to the trigger (verified: `dialog.test.tsx`). The combobox keeps DOM focus on the input and moves `aria-activedescendant` (APG). |
| **1.4.1 Use of Color / 1.1.1 Non-text Content** | A | **Supports** | Charts add a screen-reader summary + a data-table toggle + non-color encodings via the `AccessibleChart` primitive. |

## Formal AT pass (done)

The automated assistive-technology pass ran over the production console and remediated **58** verified screen-reader defects. Highlights, all now in the design system:
- **Accessible names:** combobox/select triggers named via `aria-labelledby`/`aria-label` (a `<label for>` does not name a button); the `Field` primitive auto-wires id + labelling + description + invalid onto plain children; cmdk palette/combobox search inputs named; data-table sort buttons keep their column identity; RadialGauge/StatusBar fold in the metric and use `%` not English "percent"; platform API-support cells `role="img"`; icon SVG `aria-hidden`.
- **Status messages (4.1.3):** `DataTable` `aria-busy` + status region; `AsyncSection` announces load/success; `EmptyState`/`ForbiddenState` `role="status"`, `ErrorState` `role="alert"`; SSE count tiles, audit verdict, privileged in-progress, per-budget loading all announce — **a live region on all 33 views**.
- **Structure:** `CardTitle` defaults to `<h2>` so page `<h1>` → section `<h2>` has no skipped levels; the full-page 404/500 views got `<main>` + an `<h1>`.
- **Contrast (1.4.3):** pairings **derived from the JSX**, both themes — not the hand-written list that preceded it, which was described as exhaustive and was not. Fixed dark `muted-foreground`/`danger` and the React-Flow attribution link (Rev 1.2); then a selected trace row at **1.17:1**, a `bg-card` class this system never defined, a hash-chip label at 3.54:1 and a separator glyph at 1.50:1. The current `CONTRAST_DEBT` ledger carries **11 watched entries between 2.37:1 and 4.48:1**: three non-text brand-fill measurements with an explicit 1.4.11 rationale and eight text pairings. Every entry is re-measured on each run — see 1.4.3 in the VPAT, now *Partially Supports*.
- **i18n:** localized the previously hard-coded English `aria-label`s (Spinner, minimap, "Suggestions", etc.) across all 7 locales.

## Known gaps / Partial (honest)

- **Human screen-reader walkthrough** (an operator listening to NVDA/JAWS/VoiceOver) was not run — only achievable off the headless CI. The automated pass verifies the platform-AX-API exposure those readers consume; the VPAT's two Functional-Performance rows (302.1/4.2.1) keep a residual human-spot-test recommendation.
- **1.4.11 resting borders** (`border-strong`, `-line`) are intentionally < 3:1 — ratified as Supports because they are decorative, not the sole identifier (focus ring is ≥ 3:1; chart strokes ≥ 3:1). See the VPAT 1.4.11 row.
- **Expanded registered surface:** all **54 authenticated feature views** are represented in the enumerated accessibility inventories, enforced by `registry.a11y-coverage.test.ts`. The 2026-08-22 release run walked all 59 inventory routes in both themes and returned zero, but `/eventing` rendered the error boundary (`TypeError: Cannot read properties of undefined (reading 'unavailable')`), so that successful gate is not evidence for the view itself. Numeric conformance over the expanded release surface is **PENDING** a clean render and ACR re-issue.

## How this is enforced

- **AT harness** `web/e2e-visual/at-run.ts`: `pnpm -C web at:gate` builds the SPA and runs axe over its enumerated route list in **both** themes, failing (exit 2) on any axe critical/serious, text-contrast miss, heading skip, missing `<h1>`, or a carried contrast debt that **regressed** or that now **passes** and was left in the ledger. A pairing it could not measure also fails, in BOTH arms: "I could not look" is not "it is clean". For a derived pairing that means a class with no rule in the shipped bundle; for the token contract it means a `var()` naming a custom property that does not exist — which is invalid-at-computed-value-time and silently falls back to the INHERITED colour, so deleting a token from the palette used to measure ~14:1 and pass. The probe now hangs under a parent pinned to a colour the palette never uses, which turns that fallback into a detectable sentinel. The companion `registry.a11y-coverage.test.ts` requires the shared AT inventory to cover all **54 registered authenticated feature views** bidirectionally; omission of a new view or addition of a dead route fails the guard. Run `pnpm -C web at` for the full report (`e2e-visual/__at__/at-report-*.json`).
- axe-core in `web/e2e-visual/a11y.spec.ts` runs with the `wcag22aa` tag (plus 2.0/2.1) and fails the build on any **critical** violation; the new-2.2 rules (e.g. `target-size`) run in a real browser.
- Per-pattern keyboard tests: `data-table.test.tsx` (grid), `tree-grid.test.tsx` (treegrid), `combobox.test.tsx` (combobox), `dialog.test.tsx` (modal focus return).
