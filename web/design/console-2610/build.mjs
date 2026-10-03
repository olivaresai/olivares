// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Generates the phase-1 console prototype pages. Run: node web/design/console-2610/build.mjs
// Static design artifact for review; not imported by the app. All data is illustrative example data.
// Product words come from the console's current i18n where they exist; new words are wrapped in
// <span class="need"> (visible with ?copy=1) and listed in WEB/COPY-NEEDS.md for Root.
import { readFileSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
const wm = readFileSync(
  join(here, '../../src/components/layout/wordmark-path.ts'),
  'utf8',
)
const WM_VIEWBOX = wm.match(/VIEW_BOX = '([^']+)'/)[1]
const WM_PATH = wm.match(/WORDMARK_PATH =\s*'([^']+)'/)[1]
const need = (s) => `<span class="need">${s}</span>`

// ── Icons (16 grid, 1.5 stroke) ──
const P = {
  now: '<path d="M2.5 9.5h3l1.2 2h2.6l1.2-2h3"/><path d="M3.8 4.2 2.5 9.5V13a1 1 0 0 0 1 1h9a1 1 0 0 0 1-1V9.5l-1.3-5.3a1 1 0 0 0-1-.7H4.8a1 1 0 0 0-1 .7Z"/>',
  sessions: '<path d="M1.5 8h2.5l2-5 4 10 2-5h2.5"/>',
  work: '<rect x="2.5" y="2" width="11" height="12.5" rx="2"/><path d="M5.5 6l1 1 2-2M5.5 10.5l1 1 2-2M10 6.5h1.5M10 11h1.5"/>',
  approvals:
    '<path d="M8 1.8 13 4v4c0 3-2.1 5.3-5 6.2C5.1 13.3 3 11 3 8V4Z"/><path d="m5.8 8 1.6 1.6L10.4 6.6"/>',
  agents:
    '<rect x="2.5" y="5" width="11" height="8.5" rx="2"/><path d="M8 2.2V5M5.8 9h.01M10.2 9h.01M6.2 11.4h3.6"/>',
  cost: '<circle cx="8" cy="8" r="6"/><path d="M10 5.8c-.4-.7-1.2-1-2-1-1.2 0-2 .6-2 1.5 0 2.2 4 1.2 4 3.4 0 .9-.9 1.5-2 1.5-.9 0-1.7-.4-2-1.1M8 3.8v.9M8 11.3v.9"/>',
  map: '<circle cx="3.5" cy="4" r="1.5"/><circle cx="3.5" cy="12" r="1.5"/><circle cx="12.5" cy="8" r="1.5"/><path d="M5 4.3c3 .3 4.3 1.6 6 3.2M5 11.7c3-.3 4.3-1.6 6-3.2"/>',
  tools:
    '<rect x="1.5" y="2.5" width="13" height="11" rx="2"/><path d="M4.5 6.2 6.6 8.1 4.5 10M8.6 10.2h3"/>',
  mcp: '<path d="M5.5 1.5v3M10.5 1.5v3M3.5 4.5h9V7a4.5 4.5 0 0 1-9 0ZM8 11.5v3"/>',
  knowledge:
    '<path d="M2.5 3.5c1.8-.9 3.8-.9 5.5.4 1.7-1.3 3.7-1.3 5.5-.4v9.5c-1.8-.9-3.8-.9-5.5.4-1.7-1.3-3.7-1.3-5.5-.4Z"/><path d="M8 3.9v9.5"/>',
  secrets:
    '<circle cx="5.5" cy="10.5" r="3"/><path d="m7.6 8.4 6-6M11.2 4.8l1.6 1.6"/>',
  identities:
    '<circle cx="6" cy="5.5" r="2.5"/><path d="M1.8 13.5c.4-2.4 2.1-3.8 4.2-3.8s3.8 1.4 4.2 3.8M10.5 3.2a2.5 2.5 0 0 1 0 4.6M12.3 9.9c1 .6 1.7 1.9 1.9 3.6"/>',
  policies:
    '<path d="M8 2v12M3.5 4.5h9M3.5 4.5 1.8 9a2 2 0 0 0 3.4 0ZM12.5 4.5 10.8 9a2 2 0 0 0 3.4 0ZM5.5 14h5"/>',
  audit:
    '<path d="M4 1.5h5l3.5 3.5v9.5H4Z"/><path d="M9 1.5V5h3.5M6 9.5l1.4 1.4L10 8.3"/>',
  kill: '<path d="M8 1.8v5.5"/><path d="M4.4 4.1a5.5 5.5 0 1 0 7.2 0"/>',
  areas:
    '<rect x="2" y="2" width="5" height="5" rx="1.2"/><rect x="9" y="2" width="5" height="5" rx="1.2"/><rect x="2" y="9" width="5" height="5" rx="1.2"/><rect x="9" y="9" width="5" height="5" rx="1.2"/>',
  settings:
    '<circle cx="8" cy="8" r="2.2"/><path d="M8 1.5v1.6M8 12.9v1.6M1.5 8h1.6M12.9 8h1.6M3.4 3.4l1.1 1.1M11.5 11.5l1.1 1.1M3.4 12.6l1.1-1.1M11.5 4.5l1.1-1.1"/>',
  search: '<circle cx="7" cy="7" r="4.5"/><path d="m13.5 13.5-3.2-3.2"/>',
  bell: '<path d="M4 6.5a4 4 0 0 1 8 0c0 3.5 1.5 4.5 1.5 4.5h-11S4 10 4 6.5ZM6.5 13.5a1.6 1.6 0 0 0 3 0"/>',
  help: '<circle cx="8" cy="8" r="6.2"/><path d="M6.2 6.2a1.9 1.9 0 0 1 3.6.6c0 1.3-1.8 1.6-1.8 2.7M8 11.6h.01"/>',
  plus: '<path d="M8 3v10M3 8h10"/>',
  chevron: '<path d="m5 6 3 3 3-3"/>',
  updown: '<path d="m5 6 3-3 3 3M5 10l3 3 3-3"/>',
  right: '<path d="M3 8h10M9 4l4 4-4 4"/>',
  left: '<path d="M13 8H3M7 4 3 8l4 4"/>',
  sun: '<circle cx="8" cy="8" r="3"/><path d="M8 1v1.5M8 13.5V15M1 8h1.5M13.5 8H15M3 3l1 1M12 12l1 1M3 13l1-1M12 4l1-1"/>',
  moon: '<path d="M13.5 9.6A5.8 5.8 0 0 1 6.4 2.5a5.8 5.8 0 1 0 7.1 7.1Z"/>',
  menu: '<path d="M2 4.5h12M2 8h12M2 11.5h12"/>',
  x: '<path d="m4 4 8 8M12 4l-8 8"/>',
  check: '<path d="m3 8.5 3.2 3L13 4.5"/>',
  pause: '<path d="M6 4v8M10 4v8"/>',
  stop: '<rect x="4" y="4" width="8" height="8" rx="1.5"/>',
  join: '<path d="M6 3H3.5A1.5 1.5 0 0 0 2 4.5v7A1.5 1.5 0 0 0 3.5 13H6M10 11l3-3-3-3M13 8H6"/>',
  file: '<path d="M4 1.5h5l3.5 3.5v9.5H4Z"/><path d="M9 1.5V5h3.5"/>',
  edit: '<path d="M10.5 2.5 13.5 5.5 6 13H3v-3Z"/>',
  db: '<ellipse cx="8" cy="3.6" rx="5" ry="2"/><path d="M3 3.6v8.8c0 1.1 2.2 2 5 2s5-.9 5-2V3.6M3 8c0 1.1 2.2 2 5 2s5-.9 5-2"/>',
  user: '<circle cx="8" cy="5.4" r="2.6"/><path d="M3 14c.5-2.8 2.6-4.3 5-4.3s4.5 1.5 5 4.3"/>',
  flag: '<path d="M3.5 14V2.5M3.5 3h8l-1.5 3 1.5 3h-8"/>',
  wallet:
    '<path d="M2.5 4.5h10a1 1 0 0 1 1 1v7a1 1 0 0 1-1 1h-9a1 1 0 0 1-1-1Z"/><path d="M2.5 4.5 10.5 2v2.5M10.5 9h.01"/>',
  message: '<path d="M2.5 3.5h11v8h-6l-3 2.5v-2.5h-2Z"/>',
  passkey:
    '<circle cx="6" cy="5.5" r="2.5"/><path d="M1.8 13.5c.4-2.4 2.1-3.8 4.2-3.8.9 0 1.7.2 2.4.7M12 8.5v5M12 13.5l1.5-1.2M12 11l1.2-1"/><circle cx="12" cy="7.5" r="1.4"/>',
  lock: '<rect x="3" y="7" width="10" height="7" rx="1.5"/><path d="M5.5 7V5a2.5 2.5 0 0 1 5 0v2"/>',
  ext: '<path d="M9 2.5h4.5V7M13.5 2.5 7.5 8.5M11.5 9.5v3a1 1 0 0 1-1 1h-7a1 1 0 0 1-1-1v-7a1 1 0 0 1 1-1h3"/>',
  copy: '<rect x="5" y="5" width="9" height="9" rx="2"/><path d="M11 5V3.5A1.5 1.5 0 0 0 9.5 2h-6A1.5 1.5 0 0 0 2 3.5v6A1.5 1.5 0 0 0 3.5 11H5"/>',
  panel:
    '<rect x="1.5" y="2.5" width="13" height="11" rx="2"/><path d="M10 2.5v11"/>',
  branch:
    '<circle cx="4.5" cy="3.5" r="1.5"/><circle cx="4.5" cy="12.5" r="1.5"/><circle cx="11.5" cy="5.5" r="1.5"/><path d="M4.5 5v6M11.5 7c0 2.5-3 2.5-7 4"/>',
  dots: '<path d="M3.5 8h.01M8 8h.01M12.5 8h.01"/>',
  filter: '<path d="M2 3.5h12M4.5 8h7M7 12.5h2"/>',
  mail: '<rect x="2" y="3.5" width="12" height="9" rx="1.5"/><path d="m2.5 4.5 5.5 4 5.5-4"/>',
  shield: '<path d="M8 1.8 13 4v4c0 3-2.1 5.3-5 6.2C5.1 13.3 3 11 3 8V4Z"/>',
  server:
    '<rect x="2.5" y="2.5" width="11" height="5" rx="1.2"/><rect x="2.5" y="8.5" width="11" height="5" rx="1.2"/><path d="M5 5h.01M5 11h.01"/>',
}
const I = (n, cls = 'ico') =>
  `<svg class="${cls}" viewBox="0 0 16 16" aria-hidden="true">${P[n]}</svg>`

const mark = (
  cls = 'mark',
) => `<svg class="${cls}" viewBox="3.5 2 28 28" fill="none" stroke-linecap="round" aria-hidden="true">
<path d="M16 5.5A10.5 10.5 0 0 1 16 26.5" stroke="currentColor" stroke-width="3"/><path d="M8.4 10.5H13.2" stroke="currentColor" stroke-width="2.6"/>
<path d="M8.4 14.5H15.4" stroke="#f08000" stroke-width="3.1"/><path d="M8.4 18.5H15" stroke="currentColor" stroke-width="2.6"/><path d="M8.4 22.5H13.4" stroke="currentColor" stroke-width="2.6"/></svg>`
const wordmark = `<svg class="wordmark" viewBox="${WM_VIEWBOX}" role="img" aria-label="Olivares AI"><path d="${WM_PATH}"/></svg>`

// ── Navigation: the work section, then the product's three verbs ──
const NAV = [
  [
    null,
    [
      ['now', 'Now', 'now.html', ''],
      ['sessions', 'Sessions', 'sessions.html', '3'],
      ['work', 'Work', '#', ''],
      ['approvals', 'Approvals', 'sessions.html', 'attn:1'],
    ],
  ],
  [
    'Manage',
    [
      ['agents', 'Agents', '#', ''],
      ['cost', 'Cost', '#', ''],
      ['map', 'Access map', '#', ''],
    ],
  ],
  [
    'Integrate',
    [
      ['tools', 'AI tools', '#', ''],
      ['mcp', 'MCP servers', '#', ''],
      ['knowledge', 'Knowledge', '#', ''],
      ['secrets', 'Secrets', '#', ''],
    ],
  ],
  [
    'Secure',
    [
      ['identities', 'Identities & access', 'identities.html', ''],
      ['policies', 'Policies', '#', ''],
      ['audit', 'Audit', '#', ''],
      ['kill', 'Kill switch', '#', ''],
    ],
  ],
]
const NEW_NAV_LABELS = new Set([
  'Now',
  'Approvals',
  'Cost',
  'Knowledge',
  'Identities & access',
  'Policies',
  'MCP servers',
])

function sidebar(active, counts = true) {
  const groups = NAV.map(
    ([
      label,
      items,
    ]) => `<div class="nav-group">${label ? `<div class="nav-label">${need(label)}</div>` : ''}
${items
  .map(([ic, l, href, c]) => {
    const count =
      !counts || !c
        ? ''
        : c.startsWith('attn:')
          ? `<span class="count attn">${c.slice(5)}</span>`
          : `<span class="count">${c}</span>`
    return `<a href="${href}"${l === active ? ' aria-current="page"' : ''}>${I(ic)}${NEW_NAV_LABELS.has(l) ? need(l) : l}${count}</a>`
  })
  .join('\n')}</div>`,
  ).join('\n')
  return `<aside class="side" data-side aria-label="Console">
<div class="brand">${mark()}${wordmark}</div>
<button class="scope" type="button" aria-label="Organization and workspace: Demo Estate, All workspaces"><span class="avatar">D</span><span><b>Demo Estate</b><span>All workspaces</span></span>${I('updown', 'ico chev')}</button>
<nav class="nav" aria-label="Main">${groups}</nav>
<div class="side-foot">
<a class="nav-row" href="#">${I('areas')}${need('All areas')}</a>
<a class="nav-row" href="#">${I('settings')}Settings</a>
<div class="engine-row"><span class="dot ok"></span>Engine ready<span class="mono">26.10.0</span></div>
<div class="me"><span class="face">GH</span><b>Grace Hopper</b><button class="icon-btn" type="button" aria-label="Account menu">${I('dots')}</button></div>
</div></aside>`
}

function topbar(crumbs) {
  return `<header class="bar">
<button class="icon-btn menu-btn" type="button" data-side-open aria-label="Open navigation">${I('menu')}</button>
<div class="crumbs"><span class="scope-crumb">Demo Estate</span><span class="sep scope-crumb">/</span><span class="scope-crumb">All workspaces</span><span class="sep scope-crumb">/</span><b>${crumbs}</b></div>
<div class="bar-end">
<button class="search" type="button" data-palette-open aria-label="Search or run a command">${I('search')}<span>Search or run a command</span><span class="kbd">⌘K</span></button>
<button class="icon-btn" type="button" aria-label="Notifications">${I('bell')}<span class="badge-dot"></span></button>
<button class="icon-btn" type="button" data-theme-toggle aria-label="Switch theme">${I('sun')}</button>
<button class="icon-btn" type="button" aria-label="Help">${I('help')}</button>
</div></header>`
}

const phonebar = (
  active,
) => `<nav class="phonebar" aria-label="Quick navigation">
<a href="now.html"${active === 'Now' ? ' aria-current="page"' : ''}>${I('now')}Now</a>
<a href="sessions.html"${active === 'Sessions' ? ' aria-current="page"' : ''}>${I('sessions')}Sessions</a>
<a href="sessions.html">${I('approvals')}Approvals<span class="count">1</span></a>
<a href="#" data-side-open>${I('menu')}Menu</a></nav>`

const palette = `<div class="scrim" data-scrim></div>
<div class="palette" data-palette role="dialog" aria-modal="true" aria-label="Command menu">
<input type="text" placeholder="Search sessions, people, agents, settings…" aria-label="Search">
<div class="grp"><div>Go to</div>
<a href="now.html" class="on">${I('now')}Now<span class="kbd">G N</span></a>
<a href="sessions.html">${I('sessions')}Sessions<span class="kbd">G S</span></a>
<a href="identities.html">${I('identities')}Identities &amp; access<span class="kbd">G I</span></a></div>
<div class="grp"><div>Actions</div>
<a href="sessions.html">${I('plus')}New session<span class="kbd">N</span></a>
<a href="identities.html">${I('mail')}Invite people</a>
<a href="#">${I('kill')}Engage the kill switch…</a></div></div>`

function page({
  file,
  title,
  active,
  crumbs,
  body,
  bodyClass = '',
  paletteExtra = '',
}) {
  return `<!doctype html>
<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<html lang="en">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta name="robots" content="noindex">
<title>${title} — Olivares AI</title><link rel="stylesheet" href="console.css"><script src="console.js" defer></script></head>
<body>
<div class="app">
${sidebar(active)}
<main class="panel" id="main">
${topbar(crumbs)}
<div class="scroll ${bodyClass}">${body}</div>
</main></div>
${phonebar(active)}
${paletteExtra ? palette.replace('<div class="grp"><div>Go to</div>', paletteExtra + '<div class="grp"><div>Go to</div>').replace(' class="on"', '') : palette}
</body></html>
`
}

// ── Now (returning operator) ──
const needs = [
  [
    'approvals',
    'attn',
    'MCP tool call <span class="mono">github/create_issue</span> is held',
    ['Migrate the billing connector', 'Claude Code', 'Platform'],
    'Waits for a second person',
    '2m',
    'Review',
    'sessions.html',
  ],
  [
    'message',
    'info',
    'A session asks for input',
    ['Review egress policy for the PR-reviewer agent', 'Codex', 'Security'],
    '',
    '6m',
    'Open',
    'sessions.html',
  ],
  [
    'flag',
    'bad',
    'Unreviewed write: <span class="mono">data-export-job</span> → <span class="mono">prod-postgres</span>',
    ['Access map', 'Data'],
    'Observed, not permitted',
    '14m',
    'Investigate',
    '#',
  ],
  [
    'wallet',
    'warn',
    'Team Platform reached the slow-down threshold',
    ['Budget', '82% of the $1,200 team budget'],
    '',
    '1h',
    'Open',
    '#',
  ],
]
const running = [
  [
    'Migrate the billing connector to the governed model registry',
    'Claude Code · claude-opus-4-8',
    'Platform',
    'Ada Lovelace',
    '42m',
    '$3.18',
  ],
  [
    'Review egress policy for the PR-reviewer agent',
    'Codex · gpt-4o',
    'Security',
    'Linus Pauling',
    '18m',
    '$0.92',
  ],
  [
    'Why does the nightly eval suite drift on weekends?',
    'Grok',
    'Research',
    'Mary Somerville',
    '7m',
    '$0.31',
  ],
]
const recent = [
  [
    'Add retention windows to the evidence ledger export',
    'Claude Code',
    'Platform',
    'Ended 2h ago',
    '$4.07',
  ],
  [
    'Surface the kill-switch reason in the session viewer',
    'Claude Code',
    'Platform',
    'Ended 5h ago',
    '$1.66',
  ],
  [
    "Summarize last week's denied tool calls",
    'Codex',
    'Security',
    'Ended yesterday',
    '$0.48',
  ],
]

const nowBody = `<div class="page">
<div class="page-head"><div><h1>${need('Now')}</h1><p class="sub">Demo Estate · All workspaces</p></div>
<div class="actions"><a class="btn" href="#">${I('filter')}${need('Only mine')}</a><a class="btn btn-primary" href="sessions.html">${I('plus')}New session<span class="kbd">N</span></a></div></div>
<div class="cols">
<div>
<section class="sec" aria-labelledby="needs-t"><div class="sec-head"><h2 id="needs-t">${need('Needs you')}</h2><span class="n">4</span></div>
<div class="list">${needs
  .map(
    ([
      ic,
      tone,
      t,
      meta,
      note,
      age,
      act,
      href,
    ]) => `<a class="item" href="${href}"><span class="glyph ${tone}">${I(ic)}</span>
<span><span class="t" style="display:block">${t}</span><span class="m" style="display:block">${meta.join('<span class="sep">·</span>')}${note ? `<span class="sep">·</span>${need(note)}` : ''}</span></span>
<span class="end"><span class="num c2">${age}</span><span class="btn">${need(act)}</span></span></a>`,
  )
  .join('\n')}</div></section>
<section class="sec" aria-labelledby="run-t"><div class="sec-head"><h2 id="run-t">${need('Running')}</h2><span class="n">3</span><a class="more" href="sessions.html">All sessions${I('right', 'ico ico-sm')}</a></div>
<div class="list">${running
  .map(
    ([
      t,
      p,
      w,
      who,
      d,
      c,
    ]) => `<a class="item" href="sessions.html"><span class="glyph bare"><span class="dot live"></span></span>
<span><span class="t" style="display:block">${t}</span><span class="m" style="display:block">${p}<span class="sep">·</span>${w}<span class="sep">·</span>${who}</span></span>
<span class="end"><span class="num c1">${d}</span><span class="num c2">${c}</span></span></a>`,
  )
  .join('\n')}</div></section>
<section class="sec" aria-labelledby="rec-t"><div class="sec-head"><h2 id="rec-t">Recent</h2></div>
<div class="list">${recent
  .map(
    ([
      t,
      p,
      w,
      when,
      c,
    ]) => `<a class="item" href="sessions.html"><span class="glyph bare">${I('check', 'ico ico-sm')}</span>
<span><span class="t" style="display:block">${t}</span><span class="m" style="display:block">${p}<span class="sep">·</span>${w}</span></span>
<span class="end"><span class="c1">${when}</span><span class="num c2">${c}</span></span></a>`,
  )
  .join('\n')}</div></section>
</div>
<aside class="aside" aria-label="${'Summary'}">
<div class="fact"><h3>${I('wallet', 'ico ico-sm')}Spend this month</h3><div class="big num">$1,284 <small>of $3,000</small></div>
<div class="meter"><i style="width:43%"></i><span class="tick" style="left:60%"></span><span class="tick" style="left:80%"></span></div>
<div class="kv"><div class="dim" style="font-size:12px">${need('Of each team budget')}</div><div><span class="dot warn"></span><span>Platform</span><span>82% of $1,200</span></div><div><span class="dot"></span><span>Security</span><span>41% of $600</span></div><div><span class="dot"></span><span>Research</span><span>12% of $450</span></div></div>
<a class="link" href="#">Cost${I('right', 'ico ico-sm')}</a></div>
<div class="fact"><h3>${I('shield', 'ico ico-sm')}${need('Enforcement points')}</h3>
<div class="kv"><div><span class="dot ok"></span><span>Claude Code hooks</span><span>12 hosts</span></div><div><span class="dot ok"></span><span>Model proxy</span><span>On</span></div><div><span class="dot ok"></span><span>MCP tool calls</span><span>On</span></div><div><span class="dot"></span><span>Between agents</span><span>Not configured</span></div></div>
<a class="link" href="#">Policies${I('right', 'ico ico-sm')}</a></div>
<div class="fact"><h3>${I('kill', 'ico ico-sm')}Kill switch</h3><div class="kv"><div><span class="dot ok"></span><span>${need('Not engaged')}</span><span><a class="btn btn-danger" href="#" style="height:26px">${need('Engage…')}</a></span></div></div></div>
<div class="fact"><h3>${I('areas', 'ico ico-sm')}${need('Today')}</h3>
<div class="kv"><div><span>Sessions</span><span>31</span></div><div><span>Tool calls checked</span><span>1,942</span></div><div><span>Blocked actions</span><span>3</span></div><div><span>Open findings</span><span>4</span></div></div>
<a class="link" href="#">${need('Estate dashboards')}${I('right', 'ico ico-sm')}</a></div>
</aside>
</div></div>`

// ── First hour (fresh install, first sign-in) ──
const steps = [
  [
    'done',
    'Infrastructure',
    'The control plane can reach its database. This step is verified automatically.',
    'Verified',
  ],
  ['done', 'Your workspace', 'Default workspace · selected', 'Verified'],
  [
    'current',
    'Providers',
    'Register a provider connection and test it. Local providers can work without an API key.',
    'Pending',
  ],
  [
    '',
    'Agents and the first session',
    'Choose an account home or a managed provider as the profile’s authentication source.',
    'Pending',
  ],
  [
    '',
    'Activate your first policy enforcement point',
    'Publish a managed-settings policy and confirm a host applied it.',
    'Pending',
  ],
  [
    '',
    'Register your first source',
    'Connect a content or governance source. Credentials are stored by reference.',
    'Pending',
  ],
  [
    '',
    'Invite an administrator',
    'Recommended for recovery: add a second active administrator.',
    'Pending',
  ],
]
const startBody = `<div class="page">
<div class="start">
<div>
<div class="hello"><h1>${need('Welcome, Grace')}</h1><p>${need('Seven steps take this deployment from installed to your first governed session.')}</p></div>
<div class="progress"><span class="num">2 of 7 verified</span><div class="meter"><i style="width:28.5%"></i></div></div>
<div class="proof" style="margin-bottom:14px"><div class="q">${I('passkey')}<div><b>Protected setup actions</b><p>${need('You signed in with a password. Steps that change protected settings ask for a passkey once, when you save; it stays valid for 15 minutes.')}</p></div></div>
<div class="acts"><button class="btn" type="button">${I('passkey')}${need('Add a passkey')}</button><a class="btn btn-ghost" href="#">${need('Why this is required')}</a></div></div>
<div class="steps">
${steps
  .map(
    (
      [st, t, h, s],
      k,
    ) => `<details class="step ${st}"${st === 'current' ? ' open' : ''}><summary><span class="n">${st === 'done' ? I('check', 'ico ico-sm') : k + 1}</span><span><span class="st">${t}</span><br><span class="sh">${h}</span></span>${st === 'done' ? '<span class="dim" style="font-size:12.5px">Verified</span>' : st === 'current' ? `<span class="state attn">${need('Next')}</span>` : `<span class="state">${s}</span>`}</summary>
${
  st === 'current'
    ? `<div class="step-body">
<div class="choice" role="radiogroup" aria-label="${'Authentication source'}">
<div class="opt" role="radio" aria-checked="true" tabindex="0"><b>${I('tools')}Account home<span class="tag">CLAUDE CODE</span></b><p>${need('Use the Claude Code login that already exists on this host. No API key is stored.')}</p></div>
<div class="opt" role="radio" aria-checked="false" tabindex="-1"><b>${I('secrets')}Managed provider<span class="tag">API KEY</span></b><p>${need('Register a provider connection; the credential is sealed in the store.')}</p></div>
</div>
<div class="dim" style="font-size:12.5px">${need('Or from a terminal on this host:')}</div>
<div class="cmd"><span class="dim">$</span><span class="grow">olivares profiles add claude-code --home ~/.claude</span><button class="icon-btn" type="button" aria-label="Copy">${I('copy')}</button></div>
<div style="display:flex;gap:8px"><a class="btn btn-primary" href="#">${I('plus')}Add a provider</a><a class="btn" href="#">Manage providers</a></div>
</div>`
    : ''
}</details>`,
  )
  .join('\n')}
</div>
</div>
<aside class="aside">
<div class="fact"><h3>${I('server', 'ico ico-sm')}${need('This deployment')}</h3>
<div class="kv"><div><span>Version</span><span class="mono">26.10.0</span></div><div><span>Edition</span><span>Community</span></div><div><span>Address</span><span class="mono">ops.example.internal</span></div><div><span>Store</span><span>SQLite</span></div></div></div>
<div class="fact"><h3>${I('help', 'ico ico-sm')}${need('Guides')}</h3>
<div class="help"><a href="#">${I('file')}First hour${I('ext', 'ico ico-sm')}</a><a href="#">${I('tools')}Run Claude Code with Olivares AI${I('ext', 'ico ico-sm')}</a><a href="#">${I('passkey')}Passkeys and protected settings${I('ext', 'ico ico-sm')}</a></div></div>
</aside>
</div></div>`

// ── Sessions: list + one session ──
const sessRows = [
  [
    'Needs you',
    [
      [
        'attn',
        'Migrate the billing connector to the governed model registry',
        'Claude Code · Platform · Ada Lovelace',
        '42m',
        true,
        'Held for review',
      ],
      [
        'info',
        'Review egress policy for the PR-reviewer agent',
        'Codex · Security · Linus Pauling',
        '18m',
        false,
        'Asks for input',
      ],
    ],
  ],
  [
    'Live',
    [
      [
        'live',
        'Why does the nightly eval suite drift on weekends?',
        'Grok · Research · Mary Somerville',
        '7m',
        false,
        '',
      ],
    ],
  ],
  [
    'Earlier today',
    [
      [
        'ended',
        'Add retention windows to the evidence ledger export',
        'Claude Code · Platform · Ada Lovelace',
        '2h',
        false,
        '',
      ],
      [
        'ended',
        'Surface the kill-switch reason in the session viewer',
        'Claude Code · Platform · Grace Hopper',
        '5h',
        false,
        '',
      ],
      [
        'stopped',
        'Batch re-index of the engineering wiki',
        'Codex · Data · svc_reports',
        '6h',
        false,
        'Stopped by policy',
      ],
    ],
  ],
]
const dotFor = (s) =>
  ({
    attn: '<span class="dot attn"></span>',
    info: '<span class="dot" style="background:var(--info)"></span>',
    live: '<span class="dot live"></span>',
    ended: '<span class="dot"></span>',
    stopped: '<span class="dot bad"></span>',
  })[s]
const sessList = sessRows
  .map(
    ([g, rows]) =>
      `<div class="group-label">${need(g)}</div>${rows.map(([s, t, m, age, sel, flag]) => `<div class="srow" role="option" tabindex="0" aria-selected="${sel}" data-select>${dotFor(s)}<span class="t">${t}</span><span class="age">${age}</span><span class="m">${m}</span>${flag ? `<span class="flag state ${s === 'stopped' ? 'bad' : s === 'info' ? 'info' : 'attn'}">${need(flag)}</span>` : ''}</div>`).join('')}`,
  )
  .join('')

const evs = [
  [
    '10:02',
    'user',
    '',
    `<div class="line"><span class="who">Ada Lovelace</span><span class="what">started the session</span></div><div class="detail-text">Migrate the billing connector to the governed model registry. Keep the public API; open an issue for anything that needs a schema change.</div>`,
  ],
  [
    '10:03',
    'file',
    '',
    `<div class="line"><span class="who">Claude Code</span><span class="what">read 6 files in</span><code class="cmd">services/billing/</code><span class="pol ok">${I('check')}allowed</span></div>`,
  ],
  [
    '10:05',
    'tools',
    '',
    `<div class="line"><span class="who">Claude Code</span><span class="what">ran</span><code class="cmd">go test ./services/billing/...</code><span class="pol ok">${I('check')}allowed · dev-default</span></div>`,
  ],
  [
    '10:09',
    'edit',
    '',
    `<div class="line"><span class="who">Claude Code</span><span class="what">edited 3 files</span><span class="diffstat"><span class="a">+142</span> <span class="d">−38</span></span><span class="pol ok">${I('check')}allowed</span></div>`,
  ],
  [
    '10:12',
    'approvals',
    'attn',
    `<div class="line"><span class="who">Claude Code</span><span class="what">called</span><code class="cmd">github/create_issue</code><span class="state attn" data-held-chip>${I('pause', 'ico ico-sm')}Held</span></div>
<div class="approval" data-approval><div class="q" data-approval-title>${I('approvals')}${need('Waits for a second person')}</div>
<dl><dt>Rule</dt><dd>${need('External writes need review')} <span class="dim">· mcp-external-writes</span></dd><dt>Target</dt><dd><span class="mono">github · olivaresai/billing-service</span></dd><dt>Requested by</dt><dd>Ada Lovelace (AAL3) · 2 minutes ago</dd><dt>Payload</dt><dd>Title “Billing registry: model_id becomes a foreign key” · 14 lines <a class="dim" href="#">View</a></dd></dl>
<div class="acts"><button class="btn btn-primary" type="button" data-approve>${I('check')}Approve</button><button class="btn" type="button" data-deny>Deny</button><span class="reviewer">${need('You review this: Ada cannot approve her own request.')}</span></div></div>`,
  ],
  [
    '10:13',
    'message',
    '',
    `<div class="line" data-wait-event><span class="who">Claude Code</span><span class="what">${need('is waiting for the review to continue')}</span></div>`,
  ],
]
const timeline =
  evs
    .map(
      ([t, ic, tone, body]) =>
        `<div class="ev"><span class="time">${t}</span><span class="ic ${tone}">${I(ic, 'ico ico-sm')}</span><div class="body">${body}</div></div>`,
    )
    .join('\n') + '<div data-after-decision></div>'

const sessionsBody = `<div class="work" data-work>
<section class="pane list-pane" aria-label="Sessions">
<div class="list-head"><div class="row1"><h1>Sessions</h1><span class="dim num" style="font-size:12px;margin-left:2px">6</span>
<a class="btn" href="#" style="margin-left:auto">${I('plus')}New session</a></div>
<div class="field" style="min-width:0">${I('search')}<span>${need('Search sessions, people, tools')}</span><span class="kbd" style="margin-left:auto">/</span></div>
<div style="display:flex;gap:8px;align-items:center"><div class="seg" role="group" aria-label="Show"><button type="button" aria-pressed="false">${need('Needs you')}<span class="c attn" data-needs-count>2</span></button><button type="button" aria-pressed="false">Live<span class="c">3</span></button><button type="button" aria-pressed="true">All<span class="c">6</span></button></div><button class="icon-btn" type="button" aria-label="Filter: provider, workspace, managed or observed" style="margin-left:auto">${I('filter')}</button></div></div>
<div role="listbox" aria-label="Sessions">${sessList}</div>
</section>
<section class="pane detail" aria-label="Session">
<div class="detail-head">
<div class="top"><button class="icon-btn menu-btn" type="button" data-back aria-label="Back to sessions">${I('left')}</button>
<div style="min-width:0"><h2>Migrate the billing connector to the governed model registry</h2>
<div class="meta"><span class="state attn" data-sess-state><span class="dot attn"></span>${need('Needs you')}</span><span>${I('tools', 'ico ico-sm')}Claude Code · claude-opus-4-8</span><span>${I('branch', 'ico ico-sm')}Platform</span><span>${I('user', 'ico ico-sm')}Ada Lovelace</span><span class="num">42m · $3.18</span></div></div></div>
<div class="tabs-row"><div class="tabs" role="tablist"><button class="tab" role="tab" aria-selected="true">Timeline<span class="c">6</span></button><button class="tab" role="tab" aria-selected="false">${need('Changes')}<span class="c">3</span></button><button class="tab" role="tab" aria-selected="false">Evidence</button></div>
<div class="acts"><button class="btn wide-only" type="button">${I('join')}${need('Join')}</button><button class="btn wide-only" type="button">${I('pause')}Interrupt</button><button class="btn btn-danger" type="button">${I('stop')}Stop</button><button class="btn wide-only" type="button" data-inspector-toggle aria-pressed="false" aria-label="Show context">${I('panel')}Context</button><button class="icon-btn more-only" type="button" aria-label="More: join, interrupt, context">${I('dots')}</button></div></div>
</div>
<div class="scroll" style="overflow:auto"><div class="timeline">${timeline}</div></div>
<div class="decision-bar" data-decision-bar><span class="t">${I('approvals', 'ico ico-sm')} Held · <span class="mono">github/create_issue</span></span><button class="btn btn-primary" type="button" data-approve>Approve</button><button class="btn" type="button" data-deny>Deny</button></div>
<div class="composer">${I('message')}<span class="grow">${need('Message this session…')}</span><span class="kbd">↵</span></div>
</section>
<aside class="pane inspector-pane" aria-label="Context">
<div class="inspector-head"><b>Context</b><button class="icon-btn" type="button" data-inspector-close aria-label="Close context">${I('x')}</button></div>
<div class="inspector">
<div><h3>${need('Scope when it ran')}</h3><dl class="props"><dt>Organization</dt><dd>Demo Estate</dd><dt>Workspace</dt><dd>Platform</dd><dt>Environment</dt><dd>staging-eu</dd><dt>Provider profile</dt><dd>claude-code@platform</dd><dt>Authentication</dt><dd>Account home</dd><dt>Started by</dt><dd>Ada Lovelace · AAL3</dd></dl></div>
<div><h3>${need('Resources touched')}</h3><div class="res"><div>${I('branch', 'ico ico-sm')}billing-service<span class="mode w">RW</span></div><div>${I('db', 'ico ico-sm')}prod-postgres<span class="mode">R</span></div><div>${I('mcp', 'ico ico-sm')}github · create_issue<span class="mode w">W · held</span></div></div></div>
<div><h3>Budget</h3><div class="fact" style="padding:0;border:0"><div class="kv" style="margin-top:0"><div><span>Platform, this month</span><span>82%</span></div></div><div class="meter"><i style="width:82%;background:var(--warn)"></i><span class="tick" style="left:60%"></span><span class="tick" style="left:80%"></span></div></div></div>
<div><h3>${need('References')}</h3><div style="display:grid;gap:6px;justify-items:start"><span class="copyref">sess-coder-7a3f ${I('copy', 'ico ico-sm')}</span><span class="copyref">run 01J9…4KQ2 ${I('copy', 'ico ico-sm')}</span></div></div>
</div></aside>
</div>`

// ── Identities & access ──
const people = [
  [
    'GH',
    'Grace Hopper',
    'grace@example.com',
    'Owner',
    'Platform admins',
    'Passkey',
    'AAL3',
    'Now',
    'Active',
  ],
  [
    'AL',
    'Ada Lovelace',
    'ada@example.com',
    'Admin',
    'Platform admins, Billing',
    'SSO · Okta',
    'AAL3',
    '2m ago',
    'Active',
  ],
  [
    'LP',
    'Linus Pauling',
    'linus@example.com',
    'Editor',
    'Security',
    'SSO · Okta',
    'AAL1',
    '18m ago',
    'Active',
  ],
  [
    'MS',
    'Mary Somerville',
    'mary@example.com',
    'Editor',
    'Research',
    'Password',
    'AAL1',
    '1h ago',
    'Active',
  ],
  [
    'AT',
    'Alan Turing',
    'alan@example.com',
    'Viewer',
    'Research',
    'SSO · Okta',
    'AAL1',
    'Yesterday',
    'Active',
  ],
  [
    'KJ',
    'Katherine Johnson',
    'katherine@example.com',
    'Auditor',
    '—',
    'Passkey',
    'AAL3',
    '3 days ago',
    'Active',
  ],
  [
    'RF',
    'Rosalind Franklin',
    'rosalind@example.com',
    'Editor',
    'Data',
    '—',
    '—',
    '—',
    'Invited',
  ],
]
const identitiesBody = `<div class="split" data-split><div class="page">
<div class="page-head"><div><h1>${need('Identities & access')}</h1><p class="sub">${need('Who can sign in, what they can do, and how they prove it.')}</p></div>
<div class="actions"><a class="btn btn-primary" href="#">${I('mail')}${need('Invite people')}</a></div></div>
<div class="subtabs tabs" role="tablist" style="margin-top:0">
<button class="tab" role="tab" aria-selected="true">${need('People')}<span class="c">7</span></button><button class="tab" role="tab" aria-selected="false">Groups<span class="c">5</span></button><button class="tab" role="tab" aria-selected="false">Roles<span class="c">6</span></button><button class="tab" role="tab" aria-selected="false">${need('Identity providers')}<span class="c">1</span></button><button class="tab" role="tab" aria-selected="false">${need('Service identities')}<span class="c">12</span></button><button class="tab" role="tab" aria-selected="false">API keys<span class="c">3</span></button></div>
<div class="toolbar"><div class="field">${I('search')}<span>${need('Search people')}</span></div><button class="filter" type="button">Role <b>Any</b></button><button class="filter" type="button">${need('Sign-in')} <b>Any</b></button><button class="filter" type="button">${need('Assurance')} <b>Any</b></button></div>
<table class="grid"><thead><tr><th>${need('Person')}</th><th>Role</th><th class="col-wide">Groups</th><th>${need('Sign-in')}</th><th>${need('Assurance')}</th><th class="col-wide">${need('Last active')}</th><th>Status</th></tr></thead>
<tbody>${people.map(([f, n, e, r, g, s, a, l, st], k) => `<tr data-person tabindex="0"${k === 1 ? ' aria-selected="true"' : ''}><td><div class="person"><span class="face">${f}</span><span><b>${n}</b><span>${e}</span></span></div></td><td><span class="role">${r}</span></td><td class="hide-phone col-wide"><span class="tags"><span>${g}</span></span></td><td class="hide-phone">${s}${s === 'Password' ? ` <span class="state warn" style="margin-left:6px">${need('No second factor')}</span>` : ''}</td><td class="phone-aal">${a === '—' ? '<span class="dim">—</span>' : `<span class="aal ${a === 'AAL3' ? 'hi' : ''}">${a}</span>`}</td><td class="hide-phone col-wide dim">${l}</td><td class="${st === 'Active' ? 'hide-phone' : ''}">${st === 'Active' ? '<span class="dim">Active</span>' : `<span class="state">Invited</span> <span class="dim" style="font-size:12px">${need('expires in 6 days')}</span>`}</td></tr>`).join('\n')}</tbody></table>
</div>
<div class="drawer" data-drawer role="region" aria-label="Ada Lovelace" data-open>
<div class="drawer-head"><div class="person"><span class="face">AL</span><span><b style="font-size:15px">Ada Lovelace</b><span>ada@example.com · SSO · Okta</span></span></div><button class="icon-btn" type="button" data-drawer-close aria-label="Close" style="margin-left:auto">${I('x')}</button></div>
<div class="drawer-body">
<div><h3>${need('Access')}</h3><div class="mini" data-access><div><span>Organization</span><span class="role">Admin</span></div><div><span>Platform workspace</span><span class="role" data-role-view>Admin</span><select data-role-edit hidden aria-label="Role in Platform workspace"><option>Admin</option><option>Editor</option><option>Viewer</option></select></div><div><span>Billing workspace</span><span class="role">Editor</span></div></div>
<div style="margin-top:10px;display:flex;gap:8px" data-role-actions><button class="btn" type="button" data-change-role>${I('edit')}${need('Change role')}</button><button class="btn btn-ghost" type="button">${need('Add to workspace')}</button></div>
<div style="margin-top:10px;display:flex;gap:8px" data-role-editing hidden><button class="btn" type="button" data-role-save>${need('Save changes')}</button><button class="btn btn-ghost" type="button" data-stepup-cancel>Cancel</button></div>
<div class="proof" data-stepup hidden style="margin-top:10px"><div class="q">${I('passkey')}<div><b>${need('Confirm with your passkey to save')}</b><p class="diff" data-diff>Platform workspace: <s>Admin</s> → Editor</p><p>${need('Changing a role is protected. One confirmation covers protected changes for 15 minutes.')}</p></div></div><div class="acts"><button class="btn btn-primary" type="button">${I('passkey')}${need('Use passkey')}</button><button class="btn btn-ghost" type="button" data-stepup-cancel>Cancel</button></div></div></div>
<div><h3>Groups</h3><div class="mini"><div><span>Platform admins</span><span class="dim">${need('from Okta')}</span></div><div><span>Billing</span><span class="dim">${need('assigned here')}</span></div></div></div>
<div><h3>${need('Security')}</h3><div class="mini"><div><span>Sign-in</span><span>SSO · Okta</span></div><div><span>Passkeys</span><span>1</span></div><div><span>Assurance now</span><span class="aal hi">AAL3</span></div><div><span>${need('Active sessions')}</span><span>2</span></div></div></div>
<div><h3>${need('Recent activity')}</h3><div class="mini"><div><span>${need('Requested review of')} <span class="mono">github/create_issue</span></span><span class="dim">2m</span></div><div><span>${need('Started a session in Platform')}</span><span class="dim">42m</span></div><div><span>${need('Signed in with SSO')}</span><span class="dim">1h</span></div></div>
<a class="link dim" href="#" style="display:inline-flex;gap:4px;margin-top:8px;font-size:12.5px">${need('All activity in Audit')}${I('right', 'ico ico-sm')}</a></div>
</div></div></div>`

// ── Auth pages ──
const authPage = (title, body) => `<!doctype html>
<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta name="robots" content="noindex">
<title>${title} — Olivares AI</title><link rel="stylesheet" href="console.css"><script src="console.js" defer></script></head>
<body><div class="auth"><main class="auth-main"><div class="auth-card">${body}</div></main>
<footer class="auth-foot"><span>Olivares AI 26.10.0 · Community</span><button class="icon-btn" type="button" data-theme-toggle aria-label="Switch theme" style="width:24px;height:24px">${I('sun', 'ico ico-sm')}</button></footer></div></body></html>
`
const setup = authPage(
  'Set up',
  `<div style="color:var(--mark-ink)">${mark('mark')}</div>
<h1>${need('Create the first administrator')}</h1><p class="lead">${need('This deployment has no users yet. The setup token proves you run it.')}</p>
<form class="form" action="start.html">
<label>${need('Setup token')} <span class="hint">${need('Printed in the engine log at first start.')}</span><input class="input mono" placeholder="Paste the setup token" aria-label="Setup token"></label>
<label>Name<input class="input" value="Grace Hopper"></label>
<label>Email<input class="input" value="grace@example.com"></label>
<label>Password <span class="hint">At least 8 UTF-8 bytes.</span><input class="input" type="password" value="correct horse battery"></label>
<label>Organization name <span class="hint">${need('Optional. If empty, it is called Default Organization.')}</span><input class="input" placeholder="Default Organization"></label>
<button class="btn btn-primary btn-lg" type="submit">${need('Create administrator and sign in')}</button>
<div class="note">${I('lock', 'ico ico-sm')}<span>${need('The token works once. You stay signed in; add a passkey next for protected settings.')}</span></div>
</form>`,
)
const signin = authPage(
  'Sign in',
  `<div style="color:var(--mark-ink)">${mark('mark')}</div>
<h1>Sign in</h1><p class="lead">Demo Estate · ops.example.internal</p>
<form class="form" action="now.html">
<button class="btn btn-primary btn-lg" type="submit">${I('passkey')}${need('Sign in with a passkey')}</button>
<button class="btn btn-lg" type="submit">${I('shield')}${need('Continue with Okta')}</button>
<div class="pw"><details><summary>${need('Use a password instead')}</summary><div>
<label>Email<input class="input" value="grace@example.com"></label>
<label>Password<input class="input" type="password" value="correct horse battery"></label>
<button class="btn btn-lg" type="submit">Sign in</button></div></details></div>
</form>`,
)

const out = {
  'now.html': page({
    file: 'now',
    title: 'Now',
    active: 'Now',
    crumbs: 'Now',
    body: nowBody,
  }),
  'start.html': page({
    file: 'start',
    title: 'Get started',
    active: '',
    crumbs: 'Get started',
    body: startBody,
  }),
  'sessions.html': page({
    file: 'sessions',
    title: 'Sessions',
    active: 'Sessions',
    crumbs: 'Sessions',
    body: sessionsBody,
    bodyClass: 'no-scroll',
    paletteExtra: `<div class="grp"><div>${need('This session')}</div><a href="#" class="on">${I('approvals')}${need('Approve the held call')}<span class="kbd">A</span></a><a href="#">${I('stop')}${need('Stop this session')}</a><a href="#">${I('join')}${need('Join this session')}<span class="kbd">J</span></a></div>`,
  }),
  'identities.html': page({
    file: 'identities',
    title: 'Identities & access',
    active: 'Identities & access',
    crumbs: 'Identities & access',
    body: identitiesBody,
  }),
  'setup.html': setup,
  'signin.html': signin,
}
for (const [f, html] of Object.entries(out)) writeFileSync(join(here, f), html)
console.log(
  JSON.stringify(
    Object.fromEntries(Object.entries(out).map(([f, h]) => [f, h.length])),
  ),
)
