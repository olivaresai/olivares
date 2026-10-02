// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Phase-1 prototype behaviour only: theme, command menu, list selection, inspector, approval, drawer, step-up demo.
;(() => {
  const root = document.documentElement
  const q = new URLSearchParams(location.search)
  if (q.get('theme') === 'light' || q.get('theme') === 'dark')
    root.dataset.theme = q.get('theme')
  if (q.get('copy') === '1') root.classList.add('copy-on')

  const on = (sel, ev, fn) =>
    document.addEventListener(ev, (e) => {
      const t = e.target.closest(sel)
      if (t) fn(e, t)
    })
  document.addEventListener('DOMContentLoaded', () => {
    const $ = (s) => document.querySelector(s)
    // Theme
    on('[data-theme-toggle]', 'click', () => {
      const cur =
        root.dataset.theme ||
        (matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark')
      root.dataset.theme = cur === 'light' ? 'dark' : 'light'
    })
    // Command menu (⌘K / Ctrl+K, Escape)
    const pal = $('[data-palette]'),
      scrim = $('[data-scrim]')
    const openPal = (open) => {
      if (!pal) return
      pal.toggleAttribute('data-open', open)
      scrim.toggleAttribute('data-open', open)
      if (open) pal.querySelector('input').focus()
    }
    on('[data-palette-open]', 'click', () => openPal(true))
    addEventListener('keydown', (e) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        openPal(!pal?.hasAttribute('data-open'))
      }
      if (e.key === 'Escape') {
        openPal(false)
        closeDrawer()
        side?.removeAttribute('data-open')
        setInspector(false)
      }
    })
    // Mobile navigation drawer
    const side = $('[data-side]')
    on('[data-side-open]', 'click', (e) => {
      e.preventDefault()
      side.setAttribute('data-open', '')
      scrim.setAttribute('data-open', '')
    })
    scrim?.addEventListener('click', () => {
      openPal(false)
      side?.removeAttribute('data-open')
      closeDrawer()
      scrim.removeAttribute('data-open')
    })
    // Session list selection; on phones the list and the detail are two steps
    const work = $('[data-work]')
    if (work && q.get('view') === 'list') work.classList.add('show-list')
    if (work && q.get('inspector') === '1') {
      work.classList.add('with-inspector')
      document
        .querySelectorAll('[data-inspector-toggle]')
        .forEach((b) => b.setAttribute('aria-pressed', 'true'))
    }
    on('[data-select]', 'click', (e, row) => {
      document
        .querySelectorAll('[data-select]')
        .forEach((r) => r.setAttribute('aria-selected', String(r === row)))
      work?.classList.remove('show-list')
    })
    on('[data-back]', 'click', () => work?.classList.add('show-list'))
    function setInspector(open) {
      if (!work) return
      work.classList.toggle('with-inspector', open)
      document
        .querySelectorAll('[data-inspector-toggle]')
        .forEach((b) => b.setAttribute('aria-pressed', String(open)))
    }
    on('[data-inspector-toggle]', 'click', () =>
      setInspector(!work.classList.contains('with-inspector')),
    )
    on('[data-inspector-close]', 'click', () => setInspector(false))
    // Tabs (visual only)
    on('[role="tab"]', 'click', (e, t) =>
      t.parentElement
        .querySelectorAll('[role="tab"]')
        .forEach((x) => x.setAttribute('aria-selected', String(x === t))),
    )
    on('.seg button', 'click', (e, b) =>
      b.parentElement
        .querySelectorAll('button')
        .forEach((x) => x.setAttribute('aria-pressed', String(x === b))),
    )
    on('[role="radio"]', 'click', (e, r) =>
      r.parentElement.querySelectorAll('[role="radio"]').forEach((x) => {
        x.setAttribute('aria-checked', String(x === r))
        x.tabIndex = x === r ? 0 : -1
      }),
    )
    // Approval: the decision is recorded, the card stays as evidence
    // The decision updates every place that showed the hold: card, chip, header, list, counts, timeline.
    const decide = (ok) => {
      const card = $('[data-approval]')
      if (!card || card.classList.contains('done')) return
      const word = ok ? 'Approved' : 'Denied'
      card.classList.add('done')
      if (!ok) card.classList.add('denied')
      card.querySelector('[data-approval-title]').innerHTML =
        `${word} by Grace Hopper · 10:14`
      card.querySelector('.acts').innerHTML =
        `<span class="dim">Recorded in the audit ledger · rule mcp-external-writes</span>`
      const chip = $('[data-held-chip]')
      if (chip) {
        chip.className = `state ${ok ? 'ok' : 'bad'}`
        chip.textContent = word
      }
      const st = $('[data-sess-state]')
      if (st) {
        st.className = 'state ok'
        st.innerHTML = '<span class="dot live"></span>Live'
      }
      $('[data-wait-event]')?.closest('.ev')?.remove()
      const after = $('[data-after-decision]')
      if (after)
        after.outerHTML =
          `<div class="ev"><span class="time">10:14</span><span class="ic ok"><svg class="ico ico-sm" viewBox="0 0 16 16"><path d="m3 8.5 3.2 3L13 4.5"/></svg></span><div class="body"><div class="line"><span class="who">Grace Hopper</span><span class="what">${ok ? 'approved' : 'denied'} the call</span></div></div></div>` +
          (ok
            ? `<div class="ev"><span class="time">10:14</span><span class="ic"><svg class="ico ico-sm" viewBox="0 0 16 16"><path d="M1.5 8h2.5l2-5 4 10 2-5h2.5"/></svg></span><div class="body"><div class="line"><span class="who">Claude Code</span><span class="what">resumed and created the issue</span></div></div></div>`
            : '')
      const row = document.querySelector('[data-select][aria-selected="true"]')
      if (row) {
        row.querySelector('.flag')?.remove()
        const d = row.querySelector('.dot')
        if (d) d.className = 'dot live'
      }
      document
        .querySelectorAll('.nav a .count.attn, .phonebar .count')
        .forEach((c) => {
          const n = Number(c.textContent) - 1
          if (n > 0) c.textContent = n
          else c.remove()
        })
      const nc = $('[data-needs-count]')
      if (nc) nc.textContent = Math.max(0, Number(nc.textContent) - 1)
      $('[data-decision-bar]')?.remove()
    }
    on('[data-approve]', 'click', () => decide(true))
    on('[data-deny]', 'click', () => decide(false))
    if (q.get('decided') === 'approve') decide(true)
    // People drawer and step-up at save
    const drawer = $('[data-drawer]'),
      split = $('[data-split]')
    function closeDrawer() {
      drawer?.removeAttribute('data-open')
      split?.classList.add('closed')
      document
        .querySelectorAll('[data-person]')
        .forEach((r) => r.removeAttribute('aria-selected'))
    }
    if (drawer && q.get('drawer') === '0') closeDrawer()
    on('[data-person]', 'click', (e, row) => {
      document
        .querySelectorAll('[data-person]')
        .forEach((r) => r.toggleAttribute('aria-selected', r === row))
      drawer?.setAttribute('data-open', '')
      split?.classList.remove('closed')
    })
    on('[data-drawer-close]', 'click', closeDrawer)
    // Role change: edit first, then the passkey confirms the named change at save.
    const roleEdit = (on) => {
      $('[data-role-view]').hidden = on
      $('[data-role-edit]').hidden = !on
      $('[data-role-actions]').hidden = on
      $('[data-role-editing]').hidden = !on
      if (!on) {
        $('[data-stepup]').hidden = true
        split?.classList.remove('stepup')
      }
    }
    const roleSave = () => {
      const sel = $('[data-role-edit]')
      const from = $('[data-role-view]').textContent
      if (sel.value === from) return
      $('[data-diff]').innerHTML =
        `Platform workspace: <s>${from}</s> → ${sel.value}`
      $('[data-role-editing]').hidden = true
      $('[data-stepup]').hidden = false
      split?.classList.add('stepup')
    }
    on('[data-change-role]', 'click', () => roleEdit(true))
    on('[data-role-save]', 'click', roleSave)
    on('[data-stepup-cancel]', 'click', () => roleEdit(false))
    if (q.get('stepup') === '1' && $('[data-role-edit]')) {
      roleEdit(true)
      $('[data-role-edit]').value = 'Editor'
      roleSave()
    }
  })
})()
