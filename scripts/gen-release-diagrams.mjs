#!/usr/bin/env node
// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// gen-release-diagrams.mjs — the four public diagrams of .github/assets, from their source.
//
// WHAT THIS REPLACES, and why the replacement is a program rather than a file.
// The architecture diagram this repository published showed audit sources flowing into a
// binary that serves a console, an API and a CLI. That was true and it was a quarter of the
// product: nothing in it carried the modules, the work plane agents share, the environments
// the same binary runs in, or what each edition contains. A picture that narrow teaches a
// reader the wrong shape, and a hand-drawn picture cannot be told when the shape changes.
//
// So the diagrams are DERIVED here, and three properties are enforced at generation time
// rather than reviewed by eye:
//
//   1. COLOUR IS NOT AUTHORED HERE. The palette is read from web/tokens/theme.*.tokens.json,
//      the same source the console and the transactional emails derive from. A diagram may
//      not decide what the brand looks like; move the token and the diagrams move with it.
//      Two diagram-only shades (the grouping-zone stroke and the "declared" dash) have no
//      token and are declared below with their measured contrast.
//
//   2. EVERY TEXT PAIR IS MEASURED. WCAG 2.2 contrast is computed for each label against the
//      surface it actually sits on, and anything under 4.5:1 fails the build. The predecessor
//      set zone labels and edge captions in a grey that measures 3.56:1 on light and 2.49:1
//      on dark — legible to whoever drew it, not to everyone. Structural strokes are held to
//      the 3:1 non-text threshold by the same routine.
//
//   3. ONE ORANGE PER COMPOSITION, and it has to mean something. The brand rule audited in
//      the assets ledger is enforced instead of re-audited: exactly one accented element per
//      diagram. The ledger mark inside the engine node is exempt — its orange row is part of
//      the mark, not a second finding.
//
// Text is measured, too: JetBrains Mono is a fixed-advance face, so a label that would spill
// out of its node is arithmetic, not a rendering accident, and it fails the build.
//
// Usage:
//   node scripts/gen-release-diagrams.mjs            write the SVGs
//   node scripts/gen-release-diagrams.mjs --check    0 clean · 1 the tree differs from source · 2 cannot look
//   node scripts/gen-release-diagrams.mjs --preview <file>   write a light/dark review page
//
// The three answers are the exit code, never the prose: a caller that has to parse the
// message is a caller that breaks on the first rewrite.

import { execFileSync, spawnSync } from 'node:child_process'
import crypto from 'node:crypto'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = path.dirname(fileURLToPath(import.meta.url))
const ROOT = path.resolve(HERE, '..')
const OUT_DIR = path.join(ROOT, '.github', 'assets')
// docs-site serves its own copy: an Astro page cannot reach outside public/, and a build that
// silently 404s a diagram is worse than a duplicate whose only writer is this program.
const DOCS_DIR = path.join(ROOT, 'docs-site', 'public', 'diagrams')

const args = process.argv.slice(2)
const CHECK = args.includes('--check')
const SELFTEST = args.includes('--selftest')
const previewAt = args.indexOf('--preview')
const PREVIEW = previewAt === -1 ? null : (args[previewAt + 1] || path.join(ROOT, 'diagrams-preview.html'))

function cannotLook(msg) {
  console.error(`gen-release-diagrams: CANNOT LOOK: ${msg}`)
  process.exit(2)
}

// ── the palette, read from the brand's own source ────────────────────────────────────────────

function readTokens(theme) {
  const p = path.join(ROOT, 'web', 'tokens', `theme.${theme}.tokens.json`)
  let raw
  try {
    raw = JSON.parse(fs.readFileSync(p, 'utf8'))
  } catch (e) {
    cannotLook(`${path.relative(ROOT, p)} is unreadable (${e.message}) — the palette has no source.`)
  }
  const colour = raw && raw.color
  if (!colour) cannotLook(`${path.relative(ROOT, p)} has no "color" block.`)
  const pick = (k) => {
    const v = colour[k] && colour[k].$value
    // A token that resolves to a CSS function is a value this renderer cannot flatten; saying so
    // is the third answer, and it beats emitting a colour no SVG viewer will honour.
    if (typeof v !== 'string' || !/^#[0-9a-fA-F]{6}$/.test(v)) {
      cannotLook(`token color.${k} in theme.${theme} is not a plain hex (${JSON.stringify(v)}).`)
    }
    return v.toLowerCase()
  }
  return {
    name: theme,
    bg: pick('background'),
    surface: pick('surface'),
    ink: pick('foreground'),
    dim: pick('muted-foreground'),
    accent: pick('accent'),
    accentText: pick('accent-text'),
    panel: pick('muted'),
  }
}

// The two shades with no token, declared rather than sprinkled. Both are diagram grammar rather
// than brand: `stroke` is the edge of a box, `dashed` draws what is declared and absent. Each is
// measured below against every surface it can land on, so neither is here on taste.
//
// ⛔ AND THE GROUPING DEVICE IS A FILLED PANEL, NOT A HAIRLINE, because of what the checker said
// the first time this ran: the inherited zone outline measures 1.66:1 on light and could not be
// darkened without making the grouping compete with the boxes it groups. A panel carries the
// grouping in a token fill (`muted`), and the meaning sits in its label, which is held to 4.5:1
// like every other label. Nothing informative is left resting on a line nobody can see.
//
// ⛔ AND "DECLARED, NOT BUILT" IS CARRIED BY THE DASH, NOT BY A PALER GREY. The checker rejected
// the paler grey at 3.00:1 on the panel, and there is no room between it and the solid stroke's
// 3.38:1 to be both lighter and legible. So absence is drawn with the same ink and a broken line:
// a dashed edge already carries less ink than a solid one, and the meaning stops depending on a
// reader being able to tell two greys apart.
const DIAGRAM_ONLY = {
  light: { stroke: '#86827c', dashed: '#86827c' },
  dark: { stroke: '#85858d', dashed: '#85858d' },
}

const THEMES = ['light', 'dark'].map((t) => ({ ...readTokens(t), ...DIAGRAM_ONLY[t] }))

// ── contrast, measured the way the standard defines it ───────────────────────────────────────

function luminance(hex) {
  const ch = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255)
  const lin = ch.map((c) => (c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4))
  return 0.2126 * lin[0] + 0.7152 * lin[1] + 0.0722 * lin[2]
}
function contrast(a, b) {
  const [x, y] = [luminance(a), luminance(b)].sort((p, q) => q - p)
  return (x + 0.05) / (y + 0.05)
}

const TEXT_AA = 4.5 // WCAG 2.2 1.4.3, normal text — every label in these diagrams is normal text
const NONTEXT = 3.0 // WCAG 2.2 1.4.11, graphical objects needed to understand the content

// ── the drawing grammar (the approved diagram system, ported and tightened) ───────────────────
//
//   solid fill   the product
//   outline      a system the product observes or talks to, that is not the product
//   dashed       declared and absent — drawn so a reader can see what is NOT there
//   orange       the single meaningful finding of this composition

const MONO = "'JetBrains Mono', ui-monospace, Menlo, Consolas, monospace"
const ADV = 0.6 // JetBrains Mono advance width, in em — a fixed-advance face makes fit arithmetic
const width = (s, size) => s.length * size * ADV

const esc = (s) => String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')

class Canvas {
  constructor(theme, id, w, h) {
    this.t = theme
    this.id = id
    this.w = w
    this.h = h
    this.parts = []
    this.accents = 0
    this.checks = [] // [label, fg, bg, threshold, where]
  }

  // Every colour that lands on the page goes through here, so nothing can be drawn unmeasured.
  measure(what, fg, bg, threshold) {
    this.checks.push({ what, fg, bg, threshold, ratio: contrast(fg, bg) })
  }

  text(x, y, size, fill, content, { anchor, weight, tracking, on } = {}) {
    this.measure(`text "${content}"`, fill, on || this.t.bg, TEXT_AA)
    const a = anchor ? ` text-anchor="${anchor}"` : ''
    const w = weight ? ` font-weight="${weight}"` : ''
    const l = tracking ? ` letter-spacing="${tracking}"` : ''
    this.parts.push(
      `<text x="${x}" y="${y}" font-family="${MONO}" font-size="${size}" fill="${fill}"${w}${l}${a}>${esc(content)}</text>`,
    )
    return this
  }

  // A grouping panel. Returns its own fill so the caller states what its contents sit on: a
  // label measured against the canvas while it is drawn on a panel is a measurement of the
  // wrong pair, and it would pass while the page failed.
  zone({ x, y, w, h, label }) {
    this.parts.push(
      `<rect x="${x}" y="${y}" width="${w}" height="${h}" rx="14" fill="${this.t.panel}"/>`,
    )
    // A panel may group without captioning: the editions columns are titled by the box inside
    // them, and an empty <text> node would be a label that says nothing and still ships.
    if (label) this.text(x + 18, y + 26, 11, this.t.dim, label, { tracking: 1.5, on: this.t.panel })
    return this.t.panel
  }

  node({ x, y, w, h, title, sub, variant = 'solid', accent = false, extra = '', behind }) {
    const t = this.t
    const back = behind || t.bg
    if (accent) this.accents += 1
    // The accent as a LINE ON THE CANVAS is the `accent-text` role, not the `accent` fill role.
    // The token file states the split and why: the brand orange cannot reach 3:1 as ink on a
    // near-white canvas (2.38:1 measured here), and the deepened variant exists for exactly
    // this position. Taking the fill value because it is "the brand orange" would publish a
    // finding the reader cannot see.
    const stroke = accent ? t.accentText : variant === 'dashed' ? t.dashed : t.stroke
    const fill = variant === 'solid' ? t.surface : 'none'
    const on = variant === 'solid' ? t.surface : back
    this.measure(`node stroke "${title}"`, stroke, back, NONTEXT)
    const dash = variant === 'dashed' ? ' stroke-dasharray="4 7" stroke-linecap="round"' : ''
    this.parts.push(
      `<rect x="${x}" y="${y}" width="${w}" height="${h}" rx="10" fill="${fill}" stroke="${stroke}" stroke-width="${accent ? 2 : 1.5}"${dash}/>`,
    )
    const cy = y + h / 2
    const pad = 18
    const room = w - pad * 2
    for (const [s, size] of [[title, 14], [sub, 11]]) {
      if (s && width(s, size) > room) {
        cannotLook(
          `"${s}" is ${Math.ceil(width(s, size))}px wide and its node gives it ${room}px — ` +
          'the label would spill out of the box. Shorten it or widen the node.',
        )
      }
    }
    this.text(x + pad, sub ? cy - 4 : cy + 5, 14, accent ? t.accentText : t.ink, title, { weight: 500, on })
    if (sub) this.text(x + pad, cy + 16, 11, t.dim, sub, { on })
    this.parts.push(extra)
    return this
  }

  edge({ d, state = 'plain', label, lx, ly, behind }) {
    const t = this.t
    const back = behind || t.bg
    if (state === 'flag') this.accents += 1
    const stroke = state === 'flag' ? t.accentText : state === 'declared' ? t.dashed : t.stroke
    const marker = state === 'flag' ? '-af' : state === 'declared' ? '-ad' : '-a'
    this.measure(`edge${label ? ` "${label}"` : ''}`, stroke, back, NONTEXT)
    const dash = state === 'declared' ? ' stroke-dasharray="4 7"' : ''
    this.parts.push(
      `<path d="${d}" fill="none" stroke="${stroke}" stroke-width="${state === 'flag' ? 2.4 : 1.6}"${dash} stroke-linecap="round" marker-end="url(#${this.id}${marker})"/>`,
    )
    if (label) this.text(lx, ly, 11, state === 'flag' ? t.accentText : t.dim, label, { anchor: 'middle', on: back })
    return this
  }

  // The Ledger O in miniature, as it appears on the engine node. Its orange row belongs to the
  // mark and is not a second finding, so it never touches the accent budget.
  mark(x, y) {
    const rows = [[26, this.t.ink], [38, this.t.accent], [32, this.t.ink], [22, this.t.ink]]
    this.parts.push(
      rows
        .map(([w, c], i) =>
          `<rect x="${x}" y="${y + i * 9}" width="${w}" height="5" rx="2.5" fill="${c}" opacity="${c === this.t.accent ? 1 : 0.55}"/>`)
        .join(''),
    )
    return this
  }

  // A key, which the diagrams this replaces did not carry. A grammar nobody can read is a
  // grammar that gets read wrong.
  legend(x, y, entries) {
    let cx = x
    for (const [kind, text] of entries) {
      const t = this.t
      if (kind === 'solid') {
        this.parts.push(`<rect x="${cx}" y="${y - 9}" width="14" height="12" rx="3" fill="${t.surface}" stroke="${t.stroke}" stroke-width="1.5"/>`)
      } else if (kind === 'outline') {
        this.parts.push(`<rect x="${cx}" y="${y - 9}" width="14" height="12" rx="3" fill="none" stroke="${t.stroke}" stroke-width="1.5"/>`)
      } else if (kind === 'dashed') {
        this.parts.push(`<rect x="${cx}" y="${y - 9}" width="14" height="12" rx="3" fill="none" stroke="${t.dashed}" stroke-width="1.5" stroke-dasharray="3 4"/>`)
      } else {
        this.parts.push(`<rect x="${cx}" y="${y - 9}" width="14" height="12" rx="3" fill="none" stroke="${t.accentText}" stroke-width="2"/>`)
      }
      this.text(cx + 22, y + 1, 11, this.t.dim, text, { on: this.t.bg })
      cx += 22 + width(text, 11) + 30
    }
    if (cx - 30 > this.w - x) {
      cannotLook(`the legend is ${Math.ceil(cx - 30)}px wide and the canvas gives it ${this.w - x}px.`)
    }
    return this
  }

  render() {
    for (const c of this.checks) {
      if (c.ratio + 1e-9 < c.threshold) {
        cannotLook(
          `${this.id}: ${c.what} measures ${c.ratio.toFixed(2)}:1 (${c.fg} on ${c.bg}) and the ` +
          `threshold is ${c.threshold}:1. The palette, not the picture, has to change.`,
        )
      }
    }
    const mk = (id, colour) =>
      `<marker id="${id}" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">` +
      `<path d="M 0 1.5 L 8 5 L 0 8.5" fill="none" stroke="${colour}" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/></marker>`
    return (
      `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${this.w} ${this.h}" width="${this.w}" height="${this.h}" fill="none" role="img">\n` +
      '<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->\n' +
      '<!-- SPDX-License-Identifier: AGPL-3.0-only -->\n' +
      '<!-- Generated by scripts/gen-release-diagrams.mjs. Do not edit by hand: run the generator. -->\n' +
      `<rect x="0" y="0" width="${this.w}" height="${this.h}" fill="${this.t.bg}"/>` +
      `<defs>${mk(this.id + '-a', this.t.stroke)}${mk(this.id + '-af', this.t.accent)}${mk(this.id + '-ad', this.t.dashed)}</defs>` +
      this.parts.join('') +
      '</svg>\n'
    )
  }
}

// A cubic that leaves and arrives horizontally, so edges read as flow rather than as wiring.
const bez = (x1, y1, x2, y2, k = 0.5) => {
  const dx = (x2 - x1) * k
  return `M ${x1} ${y1} C ${x1 + dx} ${y1}, ${x2 - dx} ${y2}, ${x2} ${y2}`
}

const KEY = [
  ['solid', 'the product'],
  ['outline', 'what it connects to'],
  ['dashed', 'declared, not built'],
  ['accent', 'this diagram’s finding'],
]

// ── 02 · architecture ────────────────────────────────────────────────────────────────────────
//
// Every block names a path in this repository. A block with no path is a block that cannot be
// checked, and the diagram it sits in stops being evidence.
//
//   collectors, three modes    core/runtime/loader.go (in-process and supervised plugin over
//                              AutoMTLS), core/secure/tls.go (verified-client-cert remote)
//   the engine                 cmd/olivares, console embedded by core/internal/webui/embed.go
//   modules                    cmd/olivares/wire.go
//   policy and enforcement     scripts/enforcement-seams.tsv (the four proven seams)
//   evidence                   core/audit
//   store                      core/store, Postgres FORCE row-level security
//   surfaces                   core/api (REST), core/api/grpc.go, cmd/olivares (CLI),
//                              terraform-provider-olivares
//   separate planes            cloud/control-plane, commercial/license-worker

function architecture(t, counts) {
  const c = new Canvas(t, 'd2' + t.name, 1160, 660)

  const left = c.zone({ x: 36, y: 56, w: 292, h: 468, label: 'YOUR ESTATE · COLLECTED' })
  const sources = [
    ['agent surfaces', 'claude code · codex · grok', 104],
    ['audit sources', 'pgaudit · cloudtrail · ebpf', 196],
    ['mcp & a2a peers', 'tools · servers · agents', 288],
    ['content sources', 'sharepoint · s3 · postgres', 380],
  ]
  for (const [title, sub, y] of sources) {
    c.node({ x: 58, y, w: 248, h: 76, title, sub, variant: 'outline', behind: left })
  }
  const inY = [150, 250, 350, 450]
  sources.forEach(([, , y], i) => c.edge({ d: bez(328, y + 38, 368, inY[i]) }))
  c.text(58, 498, 11, t.dim, 'collected three ways:', { on: left })
  c.text(58, 516, 11, t.dim, 'in-process · plugin · remote', { on: left })

  const mid = c.zone({ x: 370, y: 56, w: 376, h: 468, label: 'ONE SELF-HOSTED BINARY' })
  c.node({
    x: 392, y: 92, w: 332, h: 92, behind: mid,
    title: 'olivares', sub: 'one Go binary, console embedded',
  })
  c.mark(648, 116)
  c.node({ x: 392, y: 208, w: 332, h: 76, behind: mid, title: `${counts.modules} modules`, sub: 'inventory · work · access map · finops' })
  c.node({
    x: 392, y: 308, w: 332, h: 76, accent: true, behind: mid,
    title: 'policy & enforcement', sub: `cedar · ${counts.enforcement} deny-closed points`,
  })
  c.node({ x: 392, y: 408, w: 332, h: 76, behind: mid, title: 'evidence ledger', sub: 'hash-chained · ed25519 signed' })
  c.edge({ d: 'M 558 484 L 558 540' })
  c.node({
    x: 392, y: 546, w: 332, h: 58, variant: 'outline',
    // ⛔ NOT "sqlite / postgres — forced row-level security". RLS is a POSTGRES control:
    // core/store/config.go names EnginePostgres "the multi-tenant-at-scale engine with RLS" and
    // SQLite gets tenant scoping in the store API instead. One caption spanning both engines
    // hands SQLite a database guarantee it does not have.
    title: 'sqlite / postgres', sub: 'tenant-scoped · postgres adds forced rls',
  })

  const right = c.zone({ x: 790, y: 56, w: 334, h: 300, label: 'SURFACES' })
  const surfaces = [
    ['console', 'the embedded web ui', 94],
    ['rest api', 'the primary surface', 158],
    ['grpc', 'focused, frozen core subset', 222],
    ['cli & terraform', 'olivares · provider', 286],
  ]
  for (const [title, sub, y] of surfaces) c.node({ x: 812, y, w: 290, h: 54, title, sub, behind: right })
  const outY = [121, 185, 249, 313]
  surfaces.forEach((_, i) => c.edge({ d: bez(746, 150 + i * 44, 788, outY[i]) }))

  const opt = c.zone({ x: 790, y: 388, w: 334, h: 216, label: 'PLANES OF THEIR OWN · OPTIONAL' })
  // Both say built-not-deployed, which is what docs/ai-context/STATE.md measures today. An
  // outline box does not communicate "exists but is not serving", and a reader takes a drawn
  // box for a running one.
  c.node({ x: 812, y: 426, w: 290, h: 60, behind: opt, variant: 'outline', title: 'cloud control plane', sub: 'managed tenants — built, not deployed' })
  c.node({ x: 812, y: 506, w: 290, h: 60, behind: opt, variant: 'outline', title: 'license portal', sub: 'signed artifacts — built, not deployed' })
  c.edge({ d: bez(746, 420, 788, 456) })
  c.edge({ d: bez(746, 452, 788, 536) })

  c.legend(38, 636, KEY)
  return c
}

// ── 03 · how agents work together ────────────────────────────────────────────────────────────
//
//   work items                 modules/sessions/work_model.go, work_api.go
//   leases                     modules/sessions/work_lease.go
//   launch for work            modules/sessions/runtime_work_launch.go
//   messages/acks/handoffs     modules/sessions/communication_model.go, and the boot test that
//                              forbids wiring the public plane:
//                              cmd/olivares/communicationauthorityboot_test.go
//   remote delegation          cmd/olivares/orchremote.go, gated by connectors/a2a/pep.go
//   the hook                   cmd/olivares/claudehookpep.go
//   orchestration graph        modules/orchestration
//   bus                        modules/eventing
//   shadow / final authority   design only. Drawn dashed, because a reader is owed the absence.

function agents(t, counts) {
  const c = new Canvas(t, 'd3' + t.name, 1160, 660)

  const left = c.zone({ x: 36, y: 56, w: 274, h: 372, label: 'AGENTS & SESSIONS' })
  const actors = [
    ['claude code', 'governed in the hook', 96],
    ['codex · grok build', 'first-class surfaces', 188],
    ['more agent surfaces', 'cursor · goose · cline …', 280],
  ]
  for (const [title, sub, y] of actors) c.node({ x: 58, y, w: 230, h: 76, title, sub, variant: 'outline', behind: left })
  actors.forEach(([, , y]) => c.edge({ d: bez(310, y + 38, 388, 242) }))

  c.node({
    x: 36, y: 470, w: 274, h: 76, variant: 'outline',
    title: 'an authorized peer', sub: 'another engine, over a2a',
  })

  const mid = c.zone({ x: 390, y: 56, w: 344, h: 478, label: 'THE WORK PLANE · DURABLE' })
  const plane = [
    ['work item', 'brief · deps · acceptance', 96],
    ['lease', 'fenced, exactly one holder', 188],
    ['launch for work', 'reserve → lease → spawn', 280],
    ['messages · acks', 'durable, workflow-scoped', 372],
  ]
  for (const [title, sub, y] of plane) c.node({ x: 412, y, w: 300, h: 76, title, sub, behind: mid })
  for (let i = 0; i < plane.length - 1; i += 1) {
    c.edge({ d: `M 562 ${plane[i][2] + 76} L 562 ${plane[i + 1][2] - 6}`, behind: mid })
  }
  c.node({
    x: 412, y: 466, w: 300, h: 58, variant: 'dashed', behind: mid,
    title: 'final authority', sub: 'shadow + comparator — not built',
  })
  c.edge({ d: 'M 562 448 L 562 460', state: 'declared', behind: mid })

  c.edge({
    d: bez(410, 412, 312, 500, 0.55), state: 'flag',
    label: `delegation gate · 1 of ${counts.enforcement}`, lx: 296, ly: 462,
  })

  const right = c.zone({ x: 776, y: 56, w: 348, h: 478, label: 'WHAT COMES OUT OF IT' })
  const out = [
    ['orchestration graph', 'who delegated what, to whom', 96],
    ['event bus', 'in-process · core-nats bridge', 188],
    ['access map & drift', 'permitted vs. observed', 280],
    ['signed ledger', 'verifiable, with declared gaps', 372],
  ]
  for (const [title, sub, y] of out) c.node({ x: 798, y, w: 304, h: 76, title, sub, behind: right })
  out.forEach(([, , y], i) => c.edge({ d: bez(714, plane[i][2] + 38, 796, y + 38) }))
  c.node({
    x: 776, y: 556, w: 348, h: 58, variant: 'outline',
    title: 'your siem · itsm · webhook', sub: 'cef · leef · syslog · otlp · ocsf',
  })
  c.edge({ d: 'M 950 534 L 950 550' })

  c.legend(38, 636, KEY)
  return c
}

// ── 04 · the same binary, every environment ──────────────────────────────────────────────────
//
//   audiences        README.md, "Who runs it" — the four the product states and gates
//   compose          deploy/compose/docker-compose.yml (non-root, read-only, 1 CPU / 1 GiB)
//   kubernetes       deploy/manifests/install.yaml, deploy/helm
//   integrations     connectors/ — the count is derived by scripts/check-public-counts.sh
//   access map       modules/access-map — one capability among many, drawn as one of many

function environments(t, counts) {
  const c = new Canvas(t, 'd4' + t.name, 1160, 660)

  const left = c.zone({ x: 36, y: 56, w: 300, h: 468, label: 'WHO RUNS IT' })
  const who = [
    ['home server', 'homelab · loopback · 1 gib', 104],
    ['freelance · solo', 'a tenant per client', 196],
    ['team · small business', 'shared work, sso, audit', 288],
    ['regulated enterprise', 'postgres rls · ha · air-gap', 380],
  ]
  for (const [title, sub, y] of who) c.node({ x: 58, y, w: 256, h: 76, title, sub, variant: 'outline', behind: left })
  who.forEach(([, , y], i) => c.edge({ d: bez(336, y + 38, 402, [214, 258, 302, 346][i]) }))

  c.node({
    x: 404, y: 196, w: 344, h: 160, accent: true,
    // "every audience runs the same build" is README's own sentence for this table, and it is
    // the one this diagram can carry. "no edition is a different product" reads as a claim about
    // EDITIONS, where the honest answer is narrower — an enterprise build adds code under a build
    // tag and is not byte-identical to the open one. Diagram 05 is where that belongs.
    // The four rows are deployment SIZES, and across sizes the claim is exact. The headline used
    // to say "the same one binary" with the scoping left to the subtitle, and a headline that
    // needs its subtitle to stop overclaiming is still a headline that overclaims — a reader
    // takes the big line. "every size" says what the column beside it actually draws.
    title: 'one binary, every size', sub: 'the open build is the whole platform',
  })
  c.mark(672, 232)
  c.text(576, 330, 11, t.accentText, `${counts.modules} modules · ${counts.integrations} integrations`, { anchor: 'middle', on: t.surface })

  const runs = c.zone({ x: 404, y: 396, w: 344, h: 128, label: 'RUNS ON' })
  c.text(426, 456, 12, t.ink, 'linux · docker · kubernetes', { on: runs })
  c.text(426, 480, 12, t.ink, 'helm · air-gapped · cloud at launch', { on: runs })
  c.text(426, 504, 11, t.dim, 'one static binary, no mandatory egress', { on: runs })
  c.edge({ d: 'M 576 356 L 576 390' })

  const right = c.zone({ x: 800, y: 56, w: 324, h: 468, label: 'WHAT IT REACHES' })
  const reach = [
    ['model providers', 'anthropic · openai · xai · local', 96],
    ['clouds & directories', 'aws · azure · gcp · idps', 180],
    ['content sources', 'governed retrieval, deny-closed', 264],
    ['output connectors', 'siem · itsm · webhook · chat', 348],
    ['access map', 'one capability among these', 432],
  ]
  for (const [title, sub, y] of reach) c.node({ x: 822, y, w: 280, h: 68, title, sub, variant: 'outline', behind: right })
  reach.forEach(([, , y], i) => c.edge({ d: bez(750, 200 + i * 32, 820, y + 34) }))

  c.legend(38, 636, KEY)
  return c
}

// ── 05 · what each edition contains ──────────────────────────────────────────────────────────
//
// Composition only. The public README states that packaging and pricing are on request, and a
// picture that publishes prices would contradict the page it sits on. The offers, their names
// and what each one adds come from an internal design note (not shipped); the shape of the promise — additive
// add-ons, a core that is never capped from within, a subscription that is a download
// credential rather than a key — comes from docs/adr/0020.

function editions(t) {
  const c = new Canvas(t, 'd5' + t.name, 1160, 660)

  c.node({
    x: 36, y: 56, w: 1088, h: 84, accent: true,
    title: 'the agpl core is the whole platform',
    sub: 'add-ons are additive code on top — nothing open is removed, and nothing is capped from within',
  })

  const cols = [
    {
      x: 36, w: 208, title: 'community', sub: 'free · self-hosted',
      rows: ['the full agpl product', 'every module, uncapped', 'unlimited users', 'community support'],
    },
    {
      x: 256, w: 208, title: 'business', sub: 'self-serve at launch',
      // "+ reporting" reads as if the open build had no reporting. It has: what the add-on
      // brings is DEPTH behind reserved seams (cmd/olivares/wire_noenterprise.go). Saying
      // "commercial depth on" keeps the promise additive, which is what the model actually is.
      rows: ['everything in community', '+ commercial depth on:', 'reporting · onboarding', 'threat intel · pqc · nis2'],
    },
    {
      x: 476, w: 208, title: '+ regulated', sub: 'add-on',
      rows: ['everything in business', '+ retention governor', '+ worm audit archive', '+ legal hold · erasure'],
    },
    {
      x: 696, w: 208, title: 'business max', sub: 'business + all four',
      rows: ['+ runtime security', '+ compliance packs', '+ identity & scale', '+ regulated operations'],
    },
    {
      x: 916, w: 208, title: 'cloud standard', sub: 'we run it — at launch',
      // ⛔ THIS COLUMN SAID "quotas, not user caps" AND IT IS FALSE FOR CLOUD. The pricing canon
      // sets service_seats: 10 and cloud/control-plane/internal/admin/api.go enforces it through
      // billing.CheckAdmitSeat, refusing the extra seat. What is uncapped is the SELF-HOSTED
      // engine — core/auth/seatcap.go says so in as many words and distinguishes the two. A
      // diagram that flattens that distinction sells an unlimited seat count the service does
      // not offer.
      rows: ['managed control plane', 'plan quotas incl. seats', 'evidence retained for you', 'no server to operate'],
    },
  ]
  for (const col of cols) {
    const panel = c.zone({ x: col.x, y: 176, w: col.w, h: 262, label: '' })
    c.node({ x: col.x + 12, y: 196, w: col.w - 24, h: 72, title: col.title, sub: col.sub, behind: panel })
    c.edge({ d: `M ${col.x + col.w / 2} 140 L ${col.x + col.w / 2} 190` })
    col.rows.forEach((r, i) => {
      if (width(r, 11) > col.w - 36) {
        cannotLook(`"${r}" is ${Math.ceil(width(r, 11))}px and the column gives it ${col.w - 36}px.`)
      }
      c.text(col.x + 18, 306 + i * 28, 11, t.dim, r, { on: panel })
    })
  }

  const foot = c.zone({ x: 36, y: 470, w: 1088, h: 128, label: 'HOW YOU GET THE BYTES' })
  c.text(58, 528, 13, t.ink, 'a subscription is the credential you download signed artifacts with — never a key that', { on: foot })
  // ⛔ THIS LINE USED TO END "what you did not buy is not in the binary", and the canon says in as
  // many words that today's release assembles ONE artefact carrying all four add-on tags, so the
  // set cut is OFF in the release. That sentence is a claim about the DESIGN which the current
  // publisher does not deliver, and a diagram is the worst place to make one: it outlives the
  // paragraph that would have qualified it. What replaces it is the README's own claim, which is
  // true today and gated — the add-ons are absent from the OPEN binary.
  c.text(58, 554, 13, t.ink, 'unlocks code already sitting on your disk. the add-ons are absent from the open binary.', { on: foot })
  c.text(58, 580, 11, t.dim, 'self-hosted user accounts are unlimited; the cloud service applies plan quotas', { on: foot })

  c.legend(38, 636, KEY)
  return c
}

// ── the battery ──────────────────────────────────────────────────────────────────────────────
//
// A gate is proven with the defect that already happened, and every one of these fired against
// this generator during its own construction. Each case builds a scratch tree, mutates exactly
// one input, runs THIS PROGRAM in it, and asserts the exit code — so a case cannot pass because
// the harness was wrong about what it was running. The unmutated case comes first: without it, a
// battery of failures proves only that the program can fail, not that it can succeed.

function selftest() {
  let pass = 0
  let fail = 0

  const scratch = () => {
    const dir = fs.mkdtempSync(path.join(process.env.TMPDIR || os.tmpdir(), 'gendiag-'))
    // Heavy, never-mutated inputs are symlinked; anything a case rewrites is a real copy.
    fs.mkdirSync(path.join(dir, 'cmd', 'olivares'), { recursive: true })
    fs.mkdirSync(path.join(dir, 'web', 'tokens'), { recursive: true })
    fs.mkdirSync(path.join(dir, 'scripts'), { recursive: true })
    fs.symlinkSync(path.join(ROOT, 'connectors'), path.join(dir, 'connectors'))
    fs.copyFileSync(path.join(ROOT, 'cmd/olivares/wire.go'), path.join(dir, 'cmd/olivares/wire.go'))
    for (const t of ['light', 'dark']) {
      fs.copyFileSync(path.join(ROOT, `web/tokens/theme.${t}.tokens.json`), path.join(dir, `web/tokens/theme.${t}.tokens.json`))
    }
    fs.copyFileSync(path.join(ROOT, 'scripts/enforcement-seams.tsv'), path.join(dir, 'scripts/enforcement-seams.tsv'))
    // Copying the four proof files keeps the seam census answerable inside the scratch tree.
    for (const row of fs.readFileSync(path.join(ROOT, 'scripts/enforcement-seams.tsv'), 'utf8').split('\n')) {
      if (!row.trim() || row.startsWith('#')) continue
      for (const rel of [row.split('\t')[0], row.split('\t')[1]]) {
        const dst = path.join(dir, rel)
        fs.mkdirSync(path.dirname(dst), { recursive: true })
        if (!fs.existsSync(dst)) fs.copyFileSync(path.join(ROOT, rel), dst)
      }
    }
    fs.copyFileSync(path.join(ROOT, 'scripts/gen-release-diagrams.mjs'), path.join(dir, 'scripts/gen-release-diagrams.mjs'))
    execFileSync(process.execPath, [path.join(dir, 'scripts/gen-release-diagrams.mjs')], { stdio: 'ignore' })
    return dir
  }

  const run = (dir) => spawnSync(process.execPath, [path.join(dir, 'scripts/gen-release-diagrams.mjs'), '--check'], { encoding: 'utf8' }).status

  const check = (name, want, mutate) => {
    let dir
    try {
      dir = scratch()
      if (mutate) mutate(dir)
      const got = run(dir)
      const ok = got === want
      console.log(`${ok ? '  ok  ' : '  FAIL'} ${name}  want=${want} got=${got}`)
      ok ? (pass += 1) : (fail += 1)
    } catch (e) {
      console.log(`  FAIL ${name}  threw ${e.message}`)
      fail += 1
    } finally {
      if (dir) fs.rmSync(dir, { recursive: true, force: true })
    }
  }

  // The positive control. Everything below asserts a refusal; this asserts that the same tree,
  // unmutated, is accepted — otherwise a refusal proves nothing about the mutation.
  check('an untouched tree is CLEAN', 0, null)

  check('an output edited by hand is a FINDING', 1, (d) => {
    const f = path.join(d, '.github/assets/02-architecture-light.svg')
    fs.appendFileSync(f, '<!-- drift -->')
  })

  check('a missing token file CANNOT BE LOOKED AT', 2, (d) => {
    fs.rmSync(path.join(d, 'web/tokens/theme.light.tokens.json'))
  })

  // The contrast rule is the one most likely to be quietly relaxed, so it is driven from the
  // token side: move the light foreground to a grey that cannot reach 4.5:1 on the canvas.
  check('a token below 4.5:1 CANNOT BE LOOKED AT', 2, (d) => {
    const f = path.join(d, 'web/tokens/theme.light.tokens.json')
    const j = JSON.parse(fs.readFileSync(f, 'utf8'))
    j.color['muted-foreground'].$value = '#b3b0ab'
    fs.writeFileSync(f, JSON.stringify(j))
  })

  check('a seam whose proof no longer matches CANNOT BE LOOKED AT', 2, (d) => {
    const f = path.join(d, 'scripts/enforcement-seams.tsv')
    fs.writeFileSync(f, fs.readFileSync(f, 'utf8').replace('\tempty default must deny-closed\t', '\tZZZ_CANNOT_MATCH_ZZZ\t'))
  })

  check('a duplicated census row CANNOT BE LOOKED AT', 2, (d) => {
    const f = path.join(d, 'scripts/enforcement-seams.tsv')
    const body = fs.readFileSync(f, 'utf8')
    const row = body.split('\n').find((l) => l.trim() && !l.startsWith('#'))
    fs.writeFileSync(f, `${body}\n${row}`)
  })

  check('a label too wide for its node CANNOT BE LOOKED AT', 2, (d) => {
    const f = path.join(d, 'scripts/gen-release-diagrams.mjs')
    fs.writeFileSync(f, fs.readFileSync(f, 'utf8').replace(
      "title: 'evidence ledger', sub: 'hash-chained · ed25519 signed'",
      "title: 'evidence ledger', sub: 'hash-chained and ed25519 signed, with a caption far too long for the box it sits in'",
    ))
  })

  console.log(`gen-release-diagrams selftest: ${pass} passed, ${fail} failed`)
  process.exit(fail === 0 ? 0 : 1)
}

if (SELFTEST) selftest()

// ── the counts, derived rather than typed ────────────────────────────────────────────────────
//
// A number in a picture ages in silence. These come from the same places the public-counts gate
// reads, so a diagram cannot quietly disagree with the README beside it.

function derivedCounts() {
  const read = (p) => {
    try {
      return fs.readFileSync(path.join(ROOT, p), 'utf8')
    } catch (e) {
      cannotLook(`${p} is unreadable (${e.message}) — the counts have no source.`)
    }
  }
  const modules = new Set(read('cmd/olivares/wire.go').match(/"github\.com\/olivaresai\/olivares\/modules\/[a-z-]+"/g) || []).size

  let dirs
  try {
    dirs = fs.readdirSync(path.join(ROOT, 'connectors'), { withFileTypes: true })
      .filter((d) => d.isDirectory() && d.name !== 'node_modules')
  } catch (e) {
    cannotLook(`connectors/ is unreadable (${e.message}).`)
  }
  const hasGo = (d) => {
    const walk = (p) => fs.readdirSync(p, { withFileTypes: true }).some((e) => {
      if (e.isDirectory()) return e.name !== 'node_modules' && e.name !== 'testdata' && walk(path.join(p, e.name))
      return e.name.endsWith('.go')
    })
    return walk(path.join(ROOT, 'connectors', d.name))
  }
  const integrations = dirs.filter(hasGo).length

  // ⛔ THE SEAM COUNT IS THE PROVEN COUNT, NOT THE ROW COUNT, and the difference is the whole
  // reason that census exists. Counting rows would publish "four deny-closed points" from a file
  // anyone can add a line to, which is the defect scripts/enforcement-seams.tsv was written to
  // close: until 2026-08-05 both counters measured the EXISTENCE OF A FILE, so a seam that failed
  // OPEN counted the same as one that failed closed. A first draft of this generator counted rows
  // — the same shape of mistake that made me retire two other counts here, and I caught it in
  // myself only after criticising it in them. So this mirrors proven_seams() in
  // scripts/check-public-counts.sh: five non-empty tab-separated fields, no repeated seam, proof
  // or label, and the assertion must appear on an asserting line INSIDE the named function body.
  const ASSERT_CALL = /\bt\.(?:Fatal|Error)f?\(/
  const census = read('scripts/enforcement-seams.tsv')
  const seen = { seam: new Set(), proof: new Set(), label: new Set() }
  let rows = 0
  let seams = 0
  census.split('\n').forEach((raw, i) => {
    if (!raw.trim() || raw.trimStart().startsWith('#')) return
    const parts = raw.split('\t')
    if (parts.length !== 5 || !parts.every((f) => f.trim())) {
      cannotLook(`scripts/enforcement-seams.tsv:${i + 1} is malformed (want 5 non-empty tab-separated fields, got ${parts.length}).`)
    }
    rows += 1
    const [seam, test, func, assertion, label] = parts
    for (const [key, val] of [['seam', seam], ['proof', `${test}\u0000${func}`], ['label', label]]) {
      if (seen[key].has(val)) {
        cannotLook(`scripts/enforcement-seams.tsv:${i + 1} repeats a ${key} already counted — one enforcement point is one row, or the count inflates itself.`)
      }
      seen[key].add(val)
    }
    if (!fs.existsSync(path.join(ROOT, seam)) || !fs.existsSync(path.join(ROOT, test))) return
    // The closing brace is REQUIRED: a truncated function has no body to trust, and the file
    // merely mentioning the assertion elsewhere is not a proof that THIS test makes it.
    const body = new RegExp(`${func}[\\s\\S]*?^\\}`, 'm').exec(fs.readFileSync(path.join(ROOT, test), 'utf8'))
    if (!body) return
    const proven = body[0].split('\n').some((line) => new RegExp(assertion).test(line) && ASSERT_CALL.test(line))
    if (proven) seams += 1
  })
  if (rows === 0) cannotLook('scripts/enforcement-seams.tsv has no rows — an empty census is a vanished measurement, not a zero.')
  if (seams !== rows) {
    cannotLook(`${seams} of ${rows} enforcement seams still carry an intact proof. The diagram will not draw a number the census cannot stand behind.`)
  }

  // Only counts this program derives the SAME WAY scripts/check-public-counts.sh derives them.
  // A first draft also drew a content-source and an output-connector count from a substring of
  // my own choosing; it came back one short of the repository's own census because it swept in
  // connectors/contentsource, which is the shared contract library and not an integration. The
  // number was wrong in the direction that looked plausible, which is the direction that ships.
  const counts = { modules, integrations, enforcement: seams }
  for (const [k, v] of Object.entries(counts)) {
    // A count that comes back zero is a predicate that stopped matching, not a product that
    // shrank to nothing. Refusing to draw it is the only honest answer.
    if (!Number.isInteger(v) || v <= 0) cannotLook(`the derived count "${k}" came out as ${v}.`)
  }
  return counts
}

// ── assembly ─────────────────────────────────────────────────────────────────────────────────

const COUNTS = derivedCounts()

const DIAGRAMS = [
  ['02-architecture', (t) => architecture(t, COUNTS)],
  ['03-agent-communication', (t) => agents(t, COUNTS)],
  ['04-environments', (t) => environments(t, COUNTS)],
  ['05-editions', (t) => editions(t)],
]

const built = new Map()
for (const [base, make] of DIAGRAMS) {
  for (const theme of THEMES) {
    const canvas = make(theme)
    if (canvas.accents !== 1) {
      cannotLook(
        `${base}-${theme.name} has ${canvas.accents} accented element(s) and the brand rule is ` +
        'exactly one per composition, always meaningful.',
      )
    }
    built.set(`${base}-${theme.name}.svg`, canvas.render())
  }
}

const sha = (s) => crypto.createHash('sha256').update(s).digest('hex')

if (PREVIEW) {
  // The page points AT the files rather than embedding them, so it cannot drift from what
  // actually ships. Both themes are shown together: a diagram is only done when both are.
  const rel = (name) => path.relative(path.dirname(path.resolve(PREVIEW)), path.join(OUT_DIR, name))
  const rows = DIAGRAMS.map(([base]) => `
    <section>
      <h2>${base}</h2>
      <div class="pair">
        <figure><figcaption>light</figcaption><img src="${rel(`${base}-light.svg`)}" alt=""></figure>
        <figure class="dark"><figcaption>dark</figcaption><img src="${rel(`${base}-dark.svg`)}" alt=""></figure>
      </div>
    </section>`).join('')
  fs.writeFileSync(PREVIEW, `<!doctype html>
<meta charset="utf-8"><title>Release diagrams — light &amp; dark</title>
<style>
  body{margin:0;padding:32px;background:#f4f4f2;color:#28282b;
       font:14px/1.5 ui-monospace,Menlo,Consolas,monospace}
  h1{font-size:16px;letter-spacing:1.5px} h2{font-size:12px;letter-spacing:1.5px;color:#5c564e}
  section{margin:0 0 40px} .pair{display:grid;gap:16px}
  figure{margin:0;border:1px solid #d6d6d3;border-radius:14px;overflow:hidden;background:#fff}
  figure.dark{background:#28282b} figcaption{padding:8px 14px;font-size:11px;letter-spacing:1.5px;color:#5c564e}
  figure.dark figcaption{color:#aaaab3} img{display:block;width:100%}
</style>
<h1>OLIVARES AI — RELEASE DIAGRAMS</h1>
<p>Generated by <code>scripts/gen-release-diagrams.mjs</code>. Each frame is the file that ships.</p>
${rows}
`)
  console.log(`gen-release-diagrams: preview at ${path.relative(ROOT, path.resolve(PREVIEW))}`)
}

let diverged = 0
let written = 0
for (const [name, body] of built) {
  for (const dir of [OUT_DIR, DOCS_DIR]) {
    const dest = path.join(dir, name)
    const prev = fs.existsSync(dest) ? fs.readFileSync(dest, 'utf8') : null
    if (prev === body) continue
    diverged += 1
    if (CHECK) {
      console.error(
        `gen-release-diagrams: ${path.relative(ROOT, dest)} differs from its source ` +
        `(tree=${prev ? sha(prev).slice(0, 12) : 'ABSENT'} source=${sha(body).slice(0, 12)})`,
      )
    } else {
      fs.mkdirSync(dir, { recursive: true })
      fs.writeFileSync(dest, body)
      written += 1
    }
  }
}

if (CHECK) {
  if (diverged > 0) {
    console.error(`gen-release-diagrams: ${diverged} file(s) differ. Run \`node scripts/gen-release-diagrams.mjs\`.`)
    process.exit(1)
  }
  console.log(`gen-release-diagrams: OK — ${built.size} diagram(s) in 2 locations match their source.`)
} else {
  console.log(`gen-release-diagrams: wrote ${written} of ${built.size * 2} file(s).`)
  for (const [name, body] of built) console.log(`  ${name}  ${sha(body).slice(0, 12)}  ${body.length} bytes`)
}
