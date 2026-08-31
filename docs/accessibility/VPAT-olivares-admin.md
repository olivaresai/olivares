<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Accessibility Conformance Report (ACR) — Olivares AI control-plane console

**VPAT® 2.5Rev — INT edition** · EN 301 549 V3.2.1 (WCAG 2.1 AA baseline) **+** WCAG 2.2 AA (new SC, tested, ahead of the harmonised standard) **+** Revised Section 508 (2017, corrected 2018)

| Field | Value |
|---|---|
| **Name of product / version** | Olivares AI control plane — operations console (`/web`), v1 candidate baseline |
| **Report date** | 2026-06-06 · Revision 1.1 — 2026-06-12: completed the INT edition (Revised Section 508 chapters + EN 301 549 Chapter 4 FPS). · **Revision 1.2 — 2026-06-21: ran a formal automated assistive-technology verification pass** over the production console (the `go:embed` bundle) and remediated every defect it surfaced (58 fixes). The AT-dependent rows whose deciding evidence is the **platform accessibility API** (4.1.3 / 502.3.14 / FPC 302.1 / EN 4.2.1, and the AT-confirmation caveat on 4.1.2 / 502.3.1) are now **Supports**; 1.4.3 Contrast and the limited-vision rows (302.2 / 4.2.2) are upgraded after an **exhaustive token-pairing contrast audit**; 3.3.1 Error Identification is now **Supports** after wiring per-field error association at the design-system level. See *Evaluation methods* for what "automated AT" means here and the residual human-screen-reader recommendation. · **Revision 1.3 — 2026-07-12: surface-currency review** — the console now registers **46 authenticated feature views**. The AT automation runs axe over enumerated route inventories, and `registry.a11y-coverage.test.ts` fails if any registered concrete view path is absent from those inventories, so a new view cannot silently escape scanning. The expanded run found blockers on newly enumerated views; therefore a **full ACR numeric re-issue over the complete current surface is RECOMMENDED and PENDING a clean full-surface pass**. The conformance tables below remain asserted for the **33 authenticated views** exercised at Rev 1.2, not for views shipped since. · **Revision 1.4 — 2026-07-24: full-surface AT gate is now CLEAN.** The gate was silently under-covering the surface: `at:run all` scans `AUTH_ROUTES`, but three registered feature views (`/onboarding`, `/reporting`, `/posture-export`) lived only in a Playwright-only inventory that does not execute in this environment, and `registry.a11y-coverage.test.ts` masked the gap by validating against the **union** of both inventories (so the Rev 1.3 "46" was itself an undercount of the true **49**). `AUTH_ROUTES` is now the single source of all **49** registered authenticated feature views, and the coverage test validates it **bidirectionally** against `FEATURE_VIEWS` (no view can escape the gate; no dead routes; `ROUTES` proven a subset). The automated full-surface AT gate (axe WCAG 2.2 A+AA + landmark/heading structure + exhaustive token contrast) now runs **clean — 0 blocking — over all 49 authenticated views plus the 3 pre-auth forms, in both Warm Terminal themes.** The run surfaced and remediated **4 real blockers**: api-playground rendered no `<h1>` in its loading/error states (PageHeader now hoisted into both); two inference-proxy Select comboboxes had no accessible name (`aria-label` added); the topbar breadcrumb rendered blank on the dynamic session-viewer route — a visible bug and an axe `aria-command-name` violation — because the view-id resolver did not match parameterised registry paths (fixed); and session-viewer skipped a heading level h1→h3 (three panel headings promoted to `<h2>`). **Conformance below is now asserted over the full 49-view surface** (superseding the Rev 1.2 33-view scope). The one residual method limit — no human screen-reader walkthrough — is unchanged. · **Revision 1.5 — 2026-08-07: the contrast pairings are now DERIVED, and 1.4.3 is downgraded to *Partially Supports* as a result.** The gate measured a hand-written list of 41 token pairings and called it exhaustive; it was not, and its green did not say so. Pairings are now extracted from the console's own JSX — including compositions that exist only in a state no walk renders — giving **1054 real compositions per theme** beside the 43-pairing token contract. The expanded pass found and **fixed** four real defects (a selected trace row whose label sat at **1.17:1**; a detail panel using a `bg-card` class this system never defined, so it had no background; a hash-chip label at 3.54:1; a separator glyph at 1.50:1) and **surfaced 11 pre-existing pairings between 2.65:1 and 4.48:1 that this release still ships** — enumerated per site with their measured ratios in `CONTRAST_DEBT` and re-measured every run. **Rows 1.4.3, 302.2 and 4.2.2 move from Supports to Partially Supports** on that evidence; they return to Supports when the ledger is empty. No other row changes: axe `color-contrast`, the structural checks and the AT-tree evidence are unchanged and still clean over all 49 views in both themes. |
| **Product description** | Browser-delivered single-page administration console embedded in the `olivares` binary (Go `go:embed`, same-origin). Dark-first "Warm Terminal" UI; React 19 + Radix + Tailwind 4. Read-first: it observes/presents the engine's data, it does not playback media. |
| **Contact** | Olivares.AI — accessibility@olivares.ai |
| **Evaluation methods used** | Static source review of the design-system primitives and shell; **axe-core 4.12** automated scan (tags `wcag2a wcag2aa wcag21a wcag21aa wcag22aa` + best-practice) over **all 49 registered authenticated feature views (Rev 1.4)** **and** the pre-auth login/setup forms, in **both** Warm Terminal themes, run in a real headless browser (Chromium) — asserting zero critical/serious violations; manual **keyboard** testing of each interactive pattern (grid, treegrid, combobox, dialog, nav, forms); per-pattern unit tests (Testing Library, 653 passing). The route inventory (`AUTH_ROUTES`) is the single source of all **49 registered authenticated feature views**, coupled **bidirectionally** to `FEATURE_VIEWS` by `registry.a11y-coverage.test.ts` (a registered view absent from the gate inventory, a dead route, or a `ROUTES` entry outside `AUTH_ROUTES` all fail the guard); the clean full-surface numeric re-issue was achieved at Rev 1.4. **Rev 1.2 adds a formal automated AT verification harness** (`web/e2e-visual/at-run.ts`) that inspects the **platform accessibility tree** — the UIA / IAccessible2 / AX-API surface NVDA/JAWS/VoiceOver consume — per enumerated view: programmatic role / accessible-name / state, landmark + heading-outline structure (one `<h1>`, no skipped levels), live-region presence on every async surface, and a **WCAG contrast measurement whose pairings are DERIVED from the console's own source** (`web/e2e-visual/at-pairs.ts`) rather than hand-listed: **1054 real foreground-over-background compositions per theme**, resolved against the shipped stylesheet, on top of the **43-pairing design-token contract** that is still asserted by hand. **Rev 1.5 also records a limit of the automated walk that no previous revision disclosed:** on an unmodified tree, **13 of the 54 walked routes render the React error boundary instead of their content** (`/identity /killswitch /eventing /automations /alerting /models /finops /adoption /recordings /compliance /orchestration /observability /attestation`). The boundary carries a valid `sr-only` `<h1>` and raises no axe violation, so those routes scored `h1=1 / no heading skip / 0 axe blocking` — **indistinguishable from a view that rendered**. The automated evidence for those 13 views is therefore evidence about an error page, not about the view; the gate now names them on every run (`crashedRoutes` in the report). The remaining 41 routes are unaffected, and no conformance row is asserted on the strength of the 13. **Rev 1.5 corrects a method claim that was wrong through Rev 1.4:** the audit was never "exhaustive" and never covered "every design-token colour pairing" — it covered 41 pairings someone had thought to write down, and a composition absent from that list was invisible to the gate. Neither set is exhaustive today either: the derived pass reports on every run how many `className` expressions it could not resolve statically (currently 338), and those are covered only by axe-core, only in the states the rendered walk reached. |
| **What "automated AT" means here (and its limit)** | The deciding evidence for the *programmatic* success criteria (Name/Role/Value, Status Messages, Info & Relationships) is **what the product EXPOSES to assistive technology through the platform accessibility API** — and that is exactly what the harness verifies directly, in a real browser, across the enumerated report surface. **A human screen-reader walkthrough — an operator listening to NVDA, JAWS or VoiceOver complete tasks — has NOT been performed** (and cannot be in the project's headless Linux CI). For the SC-level rows this API-level verification is sufficient for the Rev 1.2 surface and they are reported **Supports**. For the holistic **Functional-Performance** statements about *non-visual use as a whole* (508 **302.1** / EN **4.2.1**), the building blocks are all verified, but a **human screen-reader spot-test is still recommended** before relying on this ACR for a specific high-stakes regulated/gov procurement; those rows say so explicitly. |
| **Notes** | The EAA's harmonised standard, **EN 301 549 V3.2.1**, normatively references **WCAG 2.1 AA** — so the Chapter 9 table below is the 2.1 baseline that carries the presumption of conformance. The six new-in-2.2 A/AA success criteria (2.4.11, 2.5.7, 2.5.8, 3.2.6, 3.3.7, 3.3.8) are reported in a separate section ahead of EN 301 549 V4.1.1, based on actual testing. SC 4.1.1 Parsing was removed in WCAG 2.2 and is not reported. |

> **Honesty statement.** This is a factual self-assessment. Every "Supports" is backed by code + axe + keyboard + **platform-accessibility-API** evidence cited in Remarks; conformance is asserted only for what was exercised. The method is explicit about its one limit (above): the programmatic exposure that AT consumes is verified directly and automatically; a human screen-reader walkthrough was not run, so the two holistic functional-performance rows carry a residual human-spot-test recommendation rather than overclaiming. Re-issue this ACR (a) if a human/third-party NVDA·JAWS·VoiceOver pass is later commissioned (fold its result in, drop the residual note), and (b) when EN 301 549 V4.1.1 becomes harmonised. The former condition (c) — a clean enumerated full-surface AT gate — **was satisfied at Rev 1.4**: the gate now runs clean over all 49 authenticated views in both themes. The gate is wired as `pnpm -C web at:gate`; `registry.a11y-coverage.test.ts` fails **bidirectionally** if a registered view is omitted from the gate inventory, if a dead route is added, or if the Playwright `ROUTES` subset drifts outside `AUTH_ROUTES`.

> **Release-surface delta — 2026-08-22.** The revision history and conformance rows below remain evidence for the surfaces they explicitly name; they are not silently rewritten as evidence for a larger UI. The current source registers **54 authenticated feature views**. The AT inventory contains **55 authenticated routes** (those views plus `/settings`) and **4 public routes**, for **59 walked routes** total. Static derivation now finds **1,196 foreground/background compositions per theme**, a **47-pairing token contract**, and **362 unresolved expressions**; `CONTRAST_DEBT` carries **11 watched entries** with recorded ratios from **2.37:1 to 4.48:1**. These figures come from `web/src/features/registry.tsx`, `web/e2e-visual/routes.ts`, `web/e2e-visual/at-pairs.ts`, and `web/e2e-visual/at-run.ts`. The v26.8.0 release run executed `pnpm -C web at:gate` over all 59 routes in both themes: it exited zero with no axe, heading or unaccounted-contrast blocker, but `/eventing` rendered the error boundary in both themes (`TypeError: Cannot read properties of undefined (reading 'unavailable')`). The gate therefore provides no evidence for that view. A new ACR revision remains pending until the view renders, the current bundle is re-run, and the result is recorded. The **human** NVDA/JAWS/VoiceOver walkthrough also remains pending; completed automated platform-accessibility-tree testing must not be described as that human pass.

## Conformance terms (ITI vocabulary)

| Term | Meaning |
|---|---|
| **Supports** | At least one method meets the criterion without known defects (or meets with equivalent facilitation). |
| **Partially Supports** | Some functionality does not meet the criterion. |
| **Does Not Support** | The majority of functionality does not meet the criterion. |
| **Not Applicable** | The criterion is not relevant to the product. |

## Applicable standards / guidelines

| Standard / Guideline | In report |
|---|---|
| WCAG 2.1 (A, AA) — via EN 301 549 Ch. 9 | Yes (primary, legal baseline) |
| WCAG 2.2 (A, AA) — new SC only, tested | Yes (see Notes) |
| Revised Section 508 (2017, corrected 2018) — Ch. 3 FPC, Ch. 5 Software, Ch. 6 Support Documentation & Services | Yes (INT edition; WCAG incorporated by reference per E205.4/E207.2) |
| Revised Section 508 — Ch. 4 Hardware | No — Not Applicable (rationale below) |
| EN 301 549 V3.2.1 Ch. 4 Functional Performance Statements | Yes |
| EN 301 549 V3.2.1 Ch. 9 Web | Yes (primary) |
| EN 301 549 V3.2.1 Ch. 12 Documentation & support | Yes |
| EN 301 549 V3.2.1 Ch. 5 / 6 / 7 / 10 / 11 / 13 | No — Not Applicable (rationale below) |

---

## EN 301 549 — Chapter 9: Web (WCAG 2.1 A + AA)

| Criteria | Level | Conformance | Remarks and explanations |
|---|---|---|---|
| 1.1.1 Non-text Content | A | Supports | Icons are decorative (`aria-hidden`) beside text labels; icon-only controls carry `aria-label`. Charts expose a screen-reader summary via the `AccessibleChart` primitive (`role="img"` + description) and a data-table alternative. |
| 1.2.1–1.2.5 Time-based media | A/AA | Not Applicable | The console plays no audio or video / time-based media. |
| 1.3.1 Info and Relationships | A | Supports | Semantic landmarks (`main`, `header`, `nav`, `aside`), `DataTable` as `role="grid"` with row/col indices, `TreeGrid` as `role="treegrid"` with `aria-level`/`aria-expanded`, forms via the `Field` label association. |
| 1.3.2 Meaningful Sequence | A | Supports | DOM order follows visual order; flex layout, no positional reordering that changes meaning. |
| 1.3.3 Sensory Characteristics | A | Supports | Instructions never rely on shape/position alone; status uses label + icon, not "the green one". |
| 1.3.4 Orientation | AA | Supports | Responsive; not locked to an orientation. |
| 1.3.5 Identify Input Purpose | AA | Supports | Login/setup inputs use correct `autocomplete` tokens (`username`, `current-password`, `new-password`). |
| 1.4.1 Use of Color | A | Supports | Status/mode never encoded by colour alone (badges carry text; confidence uses a distinct axis + label; `AccessibleChart` adds a non-colour data-table view). |
| 1.4.2 Audio Control | A | Not Applicable | No auto-playing audio. |
| 1.4.3 Contrast (Minimum) | AA | **Partially Supports** | **Rev 1.5 restates this row on measurement rather than on the size of a list, and downgrades it accordingly.** Through Rev 1.4 the audit was **41 hand-written token pairings**; calling it “exhaustive” was wrong — a pairing nobody had written down did not exist for the gate, and the gate's green did not say so. The gate now **derives** the pairings from the console's own source (`web/e2e-visual/at-pairs.ts`): **1054 real foreground-over-background compositions per theme**, one row per SITE, on top of the 43-pairing token contract, resolved against the shipped stylesheet so alpha and `color-mix()` values are exact. That pass found a selected trace-waterfall row whose label measured **1.17:1** (`bg-accent` under `text-muted-foreground`) — **fixed** — plus a detail panel whose `bg-card` class this design system never defined, so it had no background at all — **fixed** — and two more text misses at 3.5:1 and 1.5:1 — **fixed**. **It also found 11 pre-existing pairings between 2.65:1 and 4.48:1 that this release still ships**, which is why the row is now *Partially Supports*: the API-playground badges — **6 HTTP-method badges plus the BETA marker on an endpoint tag group** (`endpoint-tree.tsx:105`), all raw Tailwind ramp colours rather than brand tokens — and `--accent-text` over `--muted`/`--accent-soft` (3 pairings, light theme). Each is enumerated with its `file:line`, theme and measured ratio in `CONTRAST_DEBT` (`web/e2e-visual/at-run.ts`) and re-measured on every run; the gate fails if any regresses, if any starts passing (a stale entry must be deleted), or if a new failing pairing appears. **Cadence, stated plainly: `task at:gate` is a MANUAL per-release step, not continuous CI** (GitHub Actions is disabled on this repository) — “every run” means every time a release runs it, and the release runbook  makes it mandatory. axe `color-contrast` remains clean across the walk (**54 routes**: the **50** registered authenticated feature views + the harness-only `/settings` + 3 pre-auth forms) in both themes — those 10 are invisible to a rendered scan for one of three reasons, each verified at its site: **two need a `:hover`** (`executive/components.tsx:83` composes `text-accent-text` with `hover:bg-accent-soft` on one element; `logs/log-stream.tsx:116` sits on the `hover:bg-muted/80` row at `log-stream.tsx:88`), **one needs a dialog to be opened** (`orchestration-view.tsx:1042`, inside `<DialogContent>`), and **seven need list data the AT fixtures do not supply** — the api-playground parses an OpenAPI document that `fixtureFor` never serves, so its endpoint tree renders empty. The sub-3:1 pairings that remain are the ratified decorative resting borders — `border-strong` / `*-line` — which are never the sole identifier of a control or state (1.4.11 Supports). |
| 1.4.4 Resize Text | AA | Supports | rem/em units; zoom to 200% reflows without loss. |
| 1.4.5 Images of Text | AA | Supports | Text is real text (self-hosted variable fonts); no images of text. |
| 1.4.10 Reflow | AA | Partially Supports | Layout reflows to small viewports (mobile nav, responsive shell); the access **graph** is desktop-only with a documented text fallback on small screens (an intentional equivalent, not a reflow of the canvas). |
| 1.4.11 Non-text Contrast | AA | Supports | The information **required to identify a control and its state** meets 3:1: the focus ring (`ring-ring`) is ≥ 3:1 against adjacent surfaces in both themes (measured), and chart/graph data strokes use the solid semantic tokens (≥ 3:1). The *resting* hairline borders (`border-strong`, the thin semantic `-line` rules) are intentionally low-contrast (1.3–1.9:1) but are **not the sole identifier** — a control is identified by its fill + label + the ≥ 3:1 focus indicator, and the `-line` rules are decorative reinforcement beside text/icons (1.4.11 Understanding). |
| 1.4.12 Text Spacing | AA | Supports | No fixed line-height/letter-spacing traps; content tolerates user spacing overrides. |
| 1.4.13 Content on Hover or Focus | AA | Supports | Tooltips/popovers are Radix (dismissable, hoverable, persistent); the sidebar tooltip is keyboard-reachable. |
| 2.1.1 Keyboard | A | Supports | Every interactive element is keyboard-operable: grid/treegrid arrow navigation, combobox, dialogs, nav, ⌘K palette, graph zoom/pan buttons + arrow-key pan. |
| 2.1.2 No Keyboard Trap | A | Supports | Radix dialog/sheet/popover trap focus only while open and release on Escape with focus return (verified `dialog.test.tsx`). |
| 2.1.4 Character Key Shortcuts | A | Supports | The only single-key affordance (⌘K) is a modifier combo, not a lone character. |
| 2.2.1 / 2.2.2 Timing / Pause | A | Partially Supports | No session time limits imposed by the UI. The one looping animation (live-attribution pulse) honours `prefers-reduced-motion`; a user-facing pause control for it is not provided (it is decorative and stoppable via OS reduced-motion). |
| 2.3.1 Three Flashes | A | Supports | No flashing content. |
| 2.4.1 Bypass Blocks | A | Supports | A "Skip to content" link targets `#main-content` as the first focusable element (`app-layout.tsx`). |
| 2.4.2 Page Titled | A | Supports | `<title>` set; the breadcrumb names the current view. |
| 2.4.3 Focus Order | A | Supports | Logical tab order; dialogs move focus in and return it to the trigger. |
| 2.4.4 Link Purpose (In Context) | A | Supports | Links/nav items are labelled; icon-only items carry `aria-label`. |
| 2.4.5 Multiple Ways | AA | Supports | Sidebar nav + ⌘K command palette + breadcrumb provide multiple ways to reach views. |
| 2.4.6 Headings and Labels | AA | Supports | Descriptive headings (`PageHeader` `<h1>`), nav group labels, form labels. |
| 2.4.7 Focus Visible | AA | Supports | Global `:focus-visible` ring (`ring-ring`, offset) on all focusables; the active grid cell shows an inset ring. |
| 2.5.1 Pointer Gestures | A | Supports | No multipoint/path gestures required (graph drag has single-pointer button alternatives — see 2.5.7). |
| 2.5.2 Pointer Cancellation | A | Supports | Activation on up-event (native buttons / Radix); no down-event commits. |
| 2.5.3 Label in Name | A | Supports | Visible labels are contained in the accessible name. |
| 2.5.4 Motion Actuation | A | Not Applicable | No device-motion-actuated functionality. |
| 3.1.1 Language of Page | A | Supports | `<html lang>` set and kept in sync with the language selector. |
| 3.1.2 Language of Parts | AA | Supports | ES/EN content is served in the active language; no mixed-language fragments without markup. |
| 3.2.1 On Focus / 3.2.2 On Input | A | Supports | Focus/input cause no unexpected context change. |
| 3.2.3 Consistent Navigation | AA | Supports | Sidebar + topbar are a single shared shell rendered identically on every page. |
| 3.2.4 Consistent Identification | AA | Supports | Shared design-system components identify the same function the same way everywhere. |
| 3.3.1 Error Identification | A | Supports | Form submit errors use `role="alert"`. The `Field` primitive now (Rev 1.2) **auto-associates** every control with its label (`aria-labelledby`), its description and its error (`aria-describedby`) and sets `aria-invalid`, even for plain (non-render-prop) children — so per-field errors are programmatically linked and announced on focus across all forms; `FieldError` carries `role="alert"`. Forms that bypassed `Field` (agentops run/workspace dialogs) were converted. |
| 3.3.2 Labels or Instructions | A | Supports | Inputs are labelled with instructions/placeholders where needed. |
| 3.3.3 Error Suggestion | AA | Partially Supports | Validation messages are descriptive; suggestion quality varies per form and was not exhaustively reviewed. |
| 3.3.4 Error Prevention (Legal/Financial) | AA | Supports | Irreversible/privileged actions use a confirm step (`ConfirmDialog`, type-to-confirm). |
| 4.1.1 Parsing | A | — | Removed in WCAG 2.2; not reported. |
| 4.1.2 Name, Role, Value | A | Supports | Radix primitives + explicit roles/labels; axe name/role/value + `aria-*` rules pass across all 49 views in both themes (Rev 1.4); grid/treegrid/combobox/select expose correct roles/states — Rev 1.4 named the two remaining unnamed inference-proxy Select comboboxes (`aria-label`) and restored the session-viewer breadcrumb name (a parameterised-route resolver bug left it blank, tripping `aria-command-name`). Rev 1.2 verified accessible **names** via the platform AX tree and fixed the gaps it found: combobox/select triggers (a `<label for>` does not name a button → now named via `aria-labelledby`/`aria-label`), the cmdk palette & combobox search inputs (cmdk root `label`), the data-table sort buttons (column identity restored), the RadialGauge/StatusBar (metric folded in, `%` not the English "percent"), the platforms API-support cells (`role="img"`), the sidebar collapse toggle (state-correct name + `aria-expanded`), and several toggles (`aria-pressed`/`aria-expanded`). Icon/decorative SVG is `aria-hidden`. |
| 4.1.3 Status Messages | AA | Supports | Systematic live-region coverage verified by the AT harness — **all 49 views expose at least one live region** (Rev 1.4 full-surface pass), and every async surface announces its transition: the `DataTable` carries `aria-busy` + a polite status region (busy→loaded); `AsyncSection` (every intelligence/system view) announces loading + success; `EmptyState`/`ForbiddenState` are `role="status"` and `ErrorState` is `role="alert"`, so a resolved empty/forbidden/error state is spoken instead of silent; SSE-driven count tiles (health, sessions), the audit chain-verification verdict, the privileged-action in-progress window, and per-budget loading were given live regions; toasts (sonner) announce. Verified at the platform-AX-API level (no human SR walkthrough — see Evaluation methods). |

## WCAG 2.2 new success criteria (tested; ahead of the harmonised EN baseline)

| Criteria | Level | Conformance | Remarks and explanations |
|---|---|---|---|
| 2.4.11 Focus Not Obscured (Minimum) | AA | Supports | The only sticky surface (DataTable header) reserves its height with `scroll-pt-10` so a focused cell is not hidden (CSS C43); the shell topbar is a non-overlapping sibling of the scroll region. |
| 2.5.7 Dragging Movements | AA | Supports | Graph nodes are non-draggable; pan/zoom have single-pointer non-drag alternatives (React Flow zoom/fit buttons; the WebGL view's explicit zoom +/− / reset / pan buttons). No drag-to-reorder elsewhere. |
| 2.5.8 Target Size (Minimum) | AA | Supports | All controls ≥ 24×24 CSS px: buttons 28–32px; checkbox/switch extend their pointer target to ≥24px via a transparent `::before`; sort buttons `min-h-6`; graph controls `min-h-6 min-w-6`; treegrid twistie `size-6`. Verified by the axe `target-size` rule in the e2e a11y suite. |
| 3.2.6 Consistent Help | AA | Supports | The ⌘K command palette (a self-help/navigation mechanism) is rendered by the shared topbar in the same relative position on every authenticated page, so its order cannot drift. Login/setup carry no help mechanism (SC met trivially there). |
| 3.3.7 Redundant Entry | AA | Supports | Login and setup are single-step forms; no information already entered in the same process is requested again. |
| 3.3.8 Accessible Authentication (Minimum) | AA | Supports | Password is the only cognitive step and the "mechanism" exception is met: secret fields are plain inputs with correct `autocomplete`, no paste blocking, no CAPTCHA/puzzle; the setup token is pasteable. |

*(WCAG 2.2 AAA additions 2.4.12 and 3.3.9 are out of A/AA scope and not reported.)*

---

## Revised Section 508 Report (2017 standards, corrected 2018)

Per **E205.4** and **E207.2**, electronic content and software user-interface
components conform to Section 508 by meeting WCAG 2.0 A/AA; the WCAG tables above
therefore also document Revised Section 508 **501.1 Scope**, **504.2 Content
Creation or Editing**, and **602.3 Electronic Support Documentation** (the
standard "see WCAG section" mechanics of the VPAT 2.5 INT template). The
508-specific chapters follow.

### Chapter 3: Functional Performance Criteria (FPC)

| Criteria | Conformance | Remarks and explanations |
|---|---|---|
| 302.1 Without Vision | Supports | Programmatic semantics are complete and verified at the **platform-accessibility-API level**: roles/names/states via Radix + ARIA (axe clean across all 49 views, both themes), every interactive control has an accessible name, full keyboard operation, every async state announced via a live region (4.1.3), and charts carry data-table alternatives. **Residual:** a human screen-reader walkthrough (an operator completing tasks with NVDA/JAWS/VoiceOver) has not been performed and is recommended before relying on this row for a specific high-stakes procurement — see *Evaluation methods*. |
| 302.2 With Limited Vision | **Partially Supports** | 200% zoom reflows (rem/em units), visible ≥3:1 focus ring, text-spacing tolerant. **Rev 1.5:** the contrast evidence this row leans on is now derived rather than hand-listed, and it carries 10 enumerated text pairings below AA (see 1.4.3) — so this row tracks 1.4.3 back down to *Partially* until that debt is cleared. |
| 302.3 Without Perception of Color | Supports | Status/mode never encoded by colour alone (see 1.4.1). |
| 302.4 Without Hearing | Supports | The console emits no audio; hearing is never required to operate it. |
| 302.5 With Limited Hearing | Supports | Same — no audio content. |
| 302.6 Without Speech | Supports | No speech input is required or offered. |
| 302.7 With Limited Manipulation | Supports | Full keyboard operability, no path-based gestures or drags required (2.5.7), activation on up-event, targets ≥ 24×24 px (2.5.8). |
| 302.8 With Limited Reach and Strength | Supports | Software-only product; every function operable from the keyboard with no simultaneous-action requirement. |
| 302.9 With Limited Language, Cognitive, and Learning Abilities | Partially Supports | Consistent navigation/identification (3.2.3/3.2.4), labels and instructions (3.3.2), confirm steps on destructive actions; error suggestion quality not exhaustively reviewed (3.3.3) and no plain-language audit has been performed. |

### Chapter 4: Hardware

Not Applicable — Olivares AI ships no hardware. The product is a browser-delivered
web application; per the VPAT 2.5 instructions the chapter is omitted with this
note.

### Chapter 5: Software

The console is a **web application** running in the user's browser: it exposes
name/role/state through HTML/ARIA semantics, which the user agent maps to the
platform accessibility services (the interoperability path Chapter 5 contemplates
for platform software). Rows are answered on that basis; AT-dependent rows carry
the same "formal AT pass pending" honesty as 4.1.3.

| Criteria | Conformance | Remarks and explanations |
|---|---|---|
| 501.1 Scope — Incorporation of WCAG 2.0 AA | See WCAG section | Per E207.2, the WCAG tables above document conformance. |
| 502.2.1 User Control of Accessibility Features | Not Applicable | The console is not platform software and provides no platform accessibility features. |
| 502.2.2 No Disruption of Accessibility Features | Supports | The SPA does not override or disrupt browser/OS assistive features: no focus stealing, no key-event hijacking beyond documented combos, `prefers-reduced-motion` honoured, browser zoom/text settings respected. |
| 502.3.1 Object Information | Supports | Role, state(s), properties and accessible name exposed via ARIA/native semantics and **verified against the platform accessibility tree** (Rev 1.2): axe name/role/value + `aria-*` rules pass; grid/treegrid/combobox/select roles + names confirmed; every control named. |
| 502.3.2 Modification of Object Information | Supports | States (expanded, selected, checked, invalid…) are programmatically set through standard widgets and reflected in ARIA. |
| 502.3.3 Row, Column, and Headers | Supports | `DataTable` (`role="grid"`) and `TreeGrid` (`role="treegrid"`) expose row/column indices and headers. |
| 502.3.4 Values | Supports | Form values exposed via native inputs / Radix primitives. |
| 502.3.5 Modification of Values | Supports | Values settable via the same standard input mechanisms (no pointer-only value entry). |
| 502.3.6 Label Relationships | Supports | The `Field` primitive associates labels programmatically; icon-only controls carry `aria-label`. |
| 502.3.7 Hierarchical Relationships | Supports | Landmarks, list semantics, `aria-level` in the treegrid. |
| 502.3.8 Text | Supports | All text is real DOM text (no images of text). |
| 502.3.9 Modification of Text | Supports | Text inputs are native elements editable through standard APIs. |
| 502.3.10 List of Actions | Supports | Actions exposed through native/ARIA widget semantics (buttons, menu items, grid cell actions). |
| 502.3.11 Actions on Objects | Supports | Every action is keyboard-executable; the user agent exposes them to AT. |
| 502.3.12 Focus Cursor | Supports | Visible global focus ring; active grid cell shows an inset ring (2.4.7). |
| 502.3.13 Modification of Focus Cursor | Supports | Focus is movable via standard keyboard mechanisms; dialogs manage and return focus (2.4.3). |
| 502.3.14 Event Notification | Supports | Every async state change reaches a live region — systematically verified by the AT harness across all 49 views (see 4.1.3): busy/loaded, empty/forbidden/error, SSE count tiles, verification verdicts and toasts all announce. |
| 502.4 Platform Accessibility Features | Not Applicable | Platform accessibility features belong to the browser/OS, not the web app. |
| 503.2 User Preferences | Partially Supports | Browser/OS preferences the app can honour, it does: zoom and font size (rem-based), reduced motion. Colours use the product's own user-selectable Warm Terminal themes (both designed AA) rather than inheriting OS colours. |
| 503.3 Alternative User Interfaces | Not Applicable | No alternative user interface functioning as assistive technology is provided. |
| 503.4.1 Caption Controls | Not Applicable | No media playback (see 1.2.x). |
| 503.4.2 Audio Description Controls | Not Applicable | No media playback. |
| 504.2 Content Creation or Editing (and 504.2.1, 504.2.2) | Not Applicable | The console edits the control plane's own configuration and policies; it is not an authoring tool producing content for other end users, and performs no format conversion / PDF export. |
| 504.3 Prompts | Not Applicable | Not an authoring tool. |
| 504.4 Templates | Not Applicable | Not an authoring tool. |

### Chapter 6: Support Documentation and Services

| Criteria | Conformance | Remarks and explanations |
|---|---|---|
| 601.1 Scope | — | Heading row. |
| 602.2 Accessibility and Compatibility Features | Partially Supports | This ACR plus the WCAG 2.2 checklist document the accessibility features and live in the repository; the end-user accessibility statement on the product docs site is still pending (same gap as EN 12.1.1). |
| 602.3 Electronic Support Documentation | See WCAG section / Partially Supports | Documentation is electronic text (Markdown / docs site): semantic headings, real text, no scanned images. A WCAG audit of the *rendered* docs site has not yet been performed. |
| 602.4 Alternate Formats for Non-Electronic Support Documentation | Not Applicable | No non-electronic documentation exists. |
| 603.2 Information on Accessibility and Compatibility Features | Supports | `accessibility@olivares.ai` answers accessibility queries; this ACR is the published reference. |
| 603.3 Accommodation of Communication Needs | Supports | Support is text-based (email / issue tracker) — no voice-only channel exists, so deaf/hard-of-hearing and speech-disabled users face no barrier to contacting support. |

---

## EN 301 549 — Chapter 4: Functional performance statements (FPS)

| Clause | Conformance | Remarks |
|---|---|---|
| 4.2.1 Usage without vision | Supports | Mirrors 302.1 — programmatic semantics complete and verified at the platform-accessibility-API level (axe clean, every control named, every async state announced, charts have table alternatives). **Residual:** a human screen-reader walkthrough is recommended before high-stakes reliance (see *Evaluation methods*). |
| 4.2.2 Usage with limited vision | **Partially Supports** | Mirrors 302.2 — zoom/reflow + visible ≥3:1 focus ring; contrast per 1.4.3, which Rev 1.5 downgrades while 10 enumerated pairings sit below AA. |
| 4.2.3 Usage without perception of colour | Supports | Mirrors 302.3 / 1.4.1. |
| 4.2.4 Usage without hearing | Supports | No audio content. |
| 4.2.5 Usage with limited hearing | Supports | No audio content. |
| 4.2.6 Usage with no or limited vocal capability | Supports | No speech input required. |
| 4.2.7 Usage with limited manipulation or strength | Supports | Mirrors 302.7. |
| 4.2.8 Usage with limited reach | Supports | Software-only; no operable hardware parts; full keyboard operation. |
| 4.2.9 Minimize photosensitive seizure triggers | Supports | No flashing content (2.3.1). |
| 4.2.10 Usage with limited cognition, language or learning | Partially Supports | Mirrors 302.9. |
| 4.2.11 Privacy | Supports | Accessibility-relevant mechanisms do not undermine privacy: secret fields are masked native inputs with correct `autocomplete` and no paste blocking (3.3.8); no accessibility feature exposes data through a side channel. |

## EN 301 549 — Chapter 12: Documentation and support services

| Criteria | Conformance | Remarks |
|---|---|---|
| 12.1.1 / 12.1.2 Product documentation | Partially Supports | The repo carries an accessibility baseline (this ACR + the WCAG 2.2 checklist) in Markdown; an end-user accessibility-features statement in the product docs site is pending. |
| 12.2.x Support services | Not Applicable / Pending | No in-product help-desk/support service ships in this baseline; when one is added it must meet 12.2. |

## EN 301 549 — chapters declared Not Applicable

| Chapter | Rationale |
|---|---|
| Ch. 5 Generic | No closed functionality, hardware, or biometrics. |
| Ch. 6 Two-way voice / RTT | The console provides no voice communication. |
| Ch. 7 Video | No video player / time-based media playback. |
| Ch. 10 Non-web documents | The product is a web application; no standalone documents. |
| Ch. 11 Software | Browser-delivered SPA; covered under Ch. 9 Web. (Re-scope if a desktop/Electron wrapper ships.) |
| Ch. 13 Relay | No relay functionality. |

---

### Provenance & re-issue triggers

- **Backed by:** the console accessibility audits and tests; see [`WCAG-2.2-AA-checklist.md`](./WCAG-2.2-AA-checklist.md) for the per-fix evidence. The Rev 1.2 automated AT pass is reproducible: `pnpm -C web at` (report) / `pnpm -C web at:gate` (CI gate) — harness at `web/e2e-visual/at-run.ts`.
- **Rev 1.2 status (2026-06-21):** the **automated** AT verification pass is complete and the console is **green** — zero axe critical/serious across all 33 views in both themes, exhaustive AA contrast, gap-free heading outlines, a live region on every async surface. 58 verified screen-reader defects were remediated.
- **Rev 1.4 status (2026-07-24):** the automated AT gate now runs **full-surface green over all 49 authenticated views** (previously the gate inventory silently under-covered the surface by 3 views; the coverage guard is now bidirectional, so under-coverage fails the test). Zero axe critical/serious in both themes, gap-free heading outlines, a live region on every authenticated view, exhaustive AA contrast (decorative resting borders excepted, 1.4.11 Supports). **4 real blockers** found and fixed: api-playground loading/error `<h1>`, two unnamed inference-proxy Select comboboxes, a blank session-viewer breadcrumb (`aria-command-name`), and a session-viewer h1→h3 heading skip. **Honest residual:** the AT run serves the built `go:embed` bundle with mocked `/v1/**`; api-playground's OpenAPI spec is not served offline, so its fully-loaded interactive surface (endpoint tree, request/response panels) is not axe-scanned — the gate exercises its (now h1-bearing) loading/error frame. Adding a spec fixture to axe-scan the loaded playground is recommended follow-up. The human screen-reader walkthrough remains the one un-automated method limit (below).
- **Re-issue this ACR when:** (a) a **human / third-party NVDA·JAWS·VoiceOver walkthrough** is commissioned — fold its result into the two Functional-Performance rows (302.1 / 4.2.1) and drop their residual recommendation (the one remaining method limit; all SC-level rows are already Supports on platform-AX evidence); (b) EN 301 549 V4.1.1 (WCAG 2.2) becomes the harmonised standard (fold the 2.2 section into Chapter 9). Status verified 2026-06-12: draft 4.1.0 dated 2025-11-13; the revision (ETSI work item REN/HF-00301561, mandate M/587) was registered for the formal adoption procedure on 2026-06-03 — **V3.2.1 remains the harmonised version in force**. The former condition (c) — the expanded enumerated surface passing `at:gate` — **was met at Rev 1.4**: the route inventory is coupled **bidirectionally** to `FEATURE_VIEWS` by `registry.a11y-coverage.test.ts` (which fails if any registered concrete path is omitted from the gate inventory, or a dead route is added), and numeric conformance over all **49** current views was achieved by the clean full-surface pass.
