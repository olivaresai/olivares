// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Draws the installed console's icons (public/icon-192.png, icon-512.png and
// apple-touch-icon.png) from the geometry of public/favicon.svg: the Ledger O on the brand's
// dark ground, scaled to sit inside the 80% circle a maskable icon keeps safe. Node's standard
// library only; run it again when the mark changes:
//
//   node scripts/pwa-icons.mjs
import { writeFileSync } from 'node:fs'
import { deflateSync } from 'node:zlib'
import { fileURLToPath } from 'node:url'

const GROUND = [0x0a, 0x0a, 0x0a]
const INK = [0xfa, 0xfa, 0xf9]
const ORANGE = [0xf0, 0x80, 0x00]

// favicon.svg, in its own units: the arc is centred on (16, 16) with radius 10.5 and runs
// clockwise from the top to the bottom; each row is a round-capped line.
const ARC = { cx: 16, cy: 16, r: 10.5, w: 3 }
const ROWS = [
  { x1: 8.4, x2: 13.2, y: 10.5, w: 2.6, color: INK },
  { x1: 8.4, x2: 15.4, y: 14.5, w: 3.1, color: ORANGE },
  { x1: 8.4, x2: 15, y: 18.5, w: 2.6, color: INK },
  { x1: 8.4, x2: 13.4, y: 22.5, w: 2.6, color: INK },
]
// The mark's own centre, which goes to the middle of the icon.
const MARK_CENTER = { x: 17.5, y: 16 }
// 40 icon-widths of the mark's units across the canvas keep its 16-unit half-diagonal inside
// 0.4 of the canvas, the radius of the maskable safe zone.
const UNITS_ACROSS = 40

const distToSegment = (px, py, x1, y1, x2, y2) => {
  const dx = x2 - x1
  const dy = y2 - y1
  const t = Math.max(
    0,
    Math.min(1, ((px - x1) * dx + (py - y1) * dy) / (dx * dx + dy * dy || 1)),
  )
  return Math.hypot(px - (x1 + t * dx), py - (y1 + t * dy))
}

// Distance to the arc's stroke centre line: the right half circle, with round caps at its ends.
const distToArc = (x, y) => {
  if (x >= ARC.cx) return Math.abs(Math.hypot(x - ARC.cx, y - ARC.cy) - ARC.r)
  return Math.min(
    Math.hypot(x - ARC.cx, y - (ARC.cy - ARC.r)),
    Math.hypot(x - ARC.cx, y - (ARC.cy + ARC.r)),
  )
}

// colorAt is the icon's colour at one point of the mark's units.
const colorAt = (x, y) => {
  for (const row of ROWS) {
    if (distToSegment(x, y, row.x1, row.y, row.x2, row.y) <= row.w / 2)
      return row.color
  }
  return distToArc(x, y) <= ARC.w / 2 ? INK : GROUND
}

const SAMPLES = 4

const render = (size) => {
  const scale = UNITS_ACROSS / size
  const raw = Buffer.alloc((size * 3 + 1) * size)
  for (let py = 0; py < size; py++) {
    raw[py * (size * 3 + 1)] = 0 // filter: none
    for (let px = 0; px < size; px++) {
      const sum = [0, 0, 0]
      for (let sy = 0; sy < SAMPLES; sy++) {
        for (let sx = 0; sx < SAMPLES; sx++) {
          const x =
            MARK_CENTER.x + (px + (sx + 0.5) / SAMPLES - size / 2) * scale
          const y =
            MARK_CENTER.y + (py + (sy + 0.5) / SAMPLES - size / 2) * scale
          const c = colorAt(x, y)
          sum[0] += c[0]
          sum[1] += c[1]
          sum[2] += c[2]
        }
      }
      const at = py * (size * 3 + 1) + 1 + px * 3
      for (let i = 0; i < 3; i++)
        raw[at + i] = Math.round(sum[i] / (SAMPLES * SAMPLES))
    }
  }
  return png(size, raw)
}

const crcTable = Array.from({ length: 256 }, (_, n) => {
  let c = n
  for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1
  return c >>> 0
})
const crc32 = (buf) => {
  let c = 0xffffffff
  for (const b of buf) c = crcTable[(c ^ b) & 0xff] ^ (c >>> 8)
  return (c ^ 0xffffffff) >>> 0
}
const chunk = (type, data) => {
  const body = Buffer.concat([Buffer.from(type, 'ascii'), data])
  const out = Buffer.alloc(body.length + 8)
  out.writeUInt32BE(data.length, 0)
  body.copy(out, 4)
  out.writeUInt32BE(crc32(body), body.length + 4)
  return out
}
const png = (size, raw) => {
  const header = Buffer.alloc(13)
  header.writeUInt32BE(size, 0)
  header.writeUInt32BE(size, 4)
  header[8] = 8 // bit depth
  header[9] = 2 // colour type: RGB
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk('IHDR', header),
    chunk('IDAT', deflateSync(raw, { level: 9 })),
    chunk('IEND', Buffer.alloc(0)),
  ])
}

const publicDir = new URL('../public/', import.meta.url)
for (const [name, size] of [
  ['icon-192.png', 192],
  ['icon-512.png', 512],
  ['apple-touch-icon.png', 180],
]) {
  writeFileSync(fileURLToPath(new URL(name, publicDir)), render(size))
}
