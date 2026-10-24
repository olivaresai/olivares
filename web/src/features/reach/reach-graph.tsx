// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The two-column reach graph (issue #2553): tools on the left, destinations on the
// right, one neutral curve per access-map edge. A destination a PENDING approval
// names renders dashed: a dashed curve from the tool whose session asked, a dashed
// stub from the mid rail when the approval names no session, and a dashed
// underline on the label when solid edges also land there — the reach is real,
// the new request is pending. Both columns are aria-hidden; the role="img" label
// speaks the same facts, destination kinds included.
import { Fragment, useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { Clock } from 'lucide-react'
import { cn } from '@/lib/utils'
import {
  KIND_LABEL_KEY,
  type ReachDestination,
  type ReachFamily,
  type ReachLink,
  type ReachModel,
} from './model'

/** Fixed row heights the SVG y-coordinates are computed against (px, applied as
 * inline heights so the geometry cannot drift with the root font size). */
const TOOL_ROW = 44
const LABEL_ROW = 28
const DEST_ROW = 44

const FAMILY_LABEL_KEY: Record<ReachFamily, string> = {
  folders: 'familyFolders',
  mcp: 'familyMcp',
  providers: 'familyProviders',
}

/** Destination groups in model order plus each row's vertical center in px. */
function destGeometry(model: ReachModel): {
  groups: { family: ReachFamily; dests: ReachDestination[] }[]
  ys: Map<string, number>
  height: number
} {
  const groups: { family: ReachFamily; dests: ReachDestination[] }[] = []
  const ys = new Map<string, number>()
  let cursor = 0
  for (const dest of model.destinations) {
    const group = groups[groups.length - 1]
    if (!group || group.family !== dest.family) {
      groups.push({ family: dest.family, dests: [] })
      cursor += LABEL_ROW
    }
    ys.set(dest.id, cursor + DEST_ROW / 2)
    cursor += DEST_ROW
    groups[groups.length - 1]!.dests.push(dest)
  }
  return { groups, ys, height: cursor }
}

/** Links grouped by their tool id. */
function byTool(links: ReachLink[]): Map<string, ReachLink[]> {
  const map = new Map<string, ReachLink[]>()
  for (const link of links) {
    const list = map.get(link.toolId)
    if (list) list.push(link)
    else map.set(link.toolId, [link])
  }
  return map
}

/** One spoken sentence per tool and line style, destinations in the graph's
 * reading order, then the waiting set no tool asked for. A waiting destination is
 * announced as waiting, never as reached; every spoken destination names its kind
 * so a screen reader can tell a folder `github` from an MCP server `github`. */
function graphLabel(
  t: (key: string, opts?: Record<string, unknown>) => string,
  model: ReachModel,
  linksByTool: Map<string, ReachLink[]>,
  waitingByTool: Map<string, ReachLink[]>,
): string {
  const destById = new Map(model.destinations.map((d) => [d.id, d]))
  const destIndex = new Map(model.destinations.map((d, i) => [d.id, i]))
  const spoken = (d: ReachDestination) =>
    `${d.ref} (${t(KIND_LABEL_KEY[d.family])})`
  const spokenList = (links: ReachLink[] | undefined) =>
    (links ?? [])
      .map((link) => destById.get(link.destinationId))
      .filter((d): d is ReachDestination => !!d)
      .sort((a, b) => destIndex.get(a.id)! - destIndex.get(b.id)!)
      .map(spoken)
      .join(', ')
  const sentences = [t('graphAriaPrefix')]
  for (const tool of model.tools) {
    const reached = spokenList(linksByTool.get(tool.id))
    if (reached)
      sentences.push(
        `${t('toolReaches', { tool: tool.name, destinations: reached })}.`,
      )
    const waits = spokenList(waitingByTool.get(tool.id))
    if (waits)
      sentences.push(
        `${t('toolWaits', { tool: tool.name, destinations: waits })}.`,
      )
  }
  const asked = new Set(model.waitingLinks.map((l) => l.destinationId))
  const waiting = model.destinations
    .filter((d) => d.waiting && !asked.has(d.id))
    .sort((a, b) =>
      a.ref.localeCompare(b.ref, undefined, { sensitivity: 'base' }),
    )
  if (waiting.length > 0) {
    sentences.push(
      `${t('waitingSummary', {
        destinations: waiting.map(spoken).join(', '),
      })}.`,
    )
  }
  return sentences.join(' ')
}

export function ReachGraph({ model }: { model: ReachModel }) {
  const { t } = useTranslation('reach')
  const {
    groups,
    ys,
    height: destHeight,
  } = useMemo(() => destGeometry(model), [model])
  const toolHeight = model.tools.length * TOOL_ROW
  const height = Math.max(destHeight, toolHeight, 1)
  const linksByTool = useMemo(() => byTool(model.links), [model])
  const waitingByTool = useMemo(() => byTool(model.waitingLinks), [model])
  const destById = useMemo(
    () => new Map(model.destinations.map((d) => [d.id, d])),
    [model],
  )
  // A waiting destination no tool line reaches gets a dashed stub from the mid rail:
  // its approval names no session, so no tool is invented for it.
  const waitingOnly = useMemo(
    () =>
      model.destinations.filter(
        (d) => d.waiting && !model.linkedDestIds.has(d.id),
      ),
    [model],
  )
  const label = useMemo(
    () => graphLabel(t, model, linksByTool, waitingByTool),
    [t, model, linksByTool, waitingByTool],
  )

  return (
    <div className="space-y-3">
      <div
        role="img"
        aria-label={label}
        className="flex items-start rounded-panel border border-line bg-surface"
      >
        <ul aria-hidden className="w-2/5 shrink-0">
          {model.tools.map((tool) => (
            <li
              key={tool.id}
              data-tool
              style={{ height: TOOL_ROW }}
              className="flex items-center border-b border-line px-3 text-body last:border-0"
            >
              <span className="truncate" title={tool.name}>
                {tool.name}
              </span>
            </li>
          ))}
        </ul>
        <svg
          aria-hidden
          // text-2 is the neutral tone that holds 3:1 against the surface on both
          // themes; graphite-400 drops to 2.7:1 on light.
          className="w-1/5 shrink-0 text-text-2"
          style={{ height }}
          viewBox={`0 0 100 ${height}`}
          preserveAspectRatio="none"
        >
          {model.tools.map((tool, toolIndex) =>
            [
              ...(linksByTool.get(tool.id) ?? []).map((link) => ({
                link,
                wait: false,
              })),
              ...(waitingByTool.get(tool.id) ?? []).map((link) => ({
                link,
                wait: true,
              })),
            ].map(({ link, wait }) => {
              const dest = destById.get(link.destinationId)
              if (!dest) return null
              const y1 = toolIndex * TOOL_ROW + TOOL_ROW / 2
              const y2 = ys.get(dest.id)!
              return (
                <path
                  key={link.id}
                  className={cn('edge', wait && 'is-wait')}
                  d={`M0 ${y1} C 45 ${y1}, 55 ${y2}, 100 ${y2}`}
                  fill="none"
                  stroke="currentColor"
                  strokeWidth={1}
                  strokeDasharray={wait ? '4 3' : undefined}
                />
              )
            }),
          )}
          {waitingOnly.map((dest) => {
            const y = ys.get(dest.id)!
            return (
              <path
                key={`wait-${dest.id}`}
                className="edge is-wait"
                d={`M50 ${y} C 74 ${y}, 88 ${y}, 100 ${y}`}
                fill="none"
                stroke="currentColor"
                strokeWidth={1}
                strokeDasharray="4 3"
              />
            )
          })}
        </svg>
        <ul aria-hidden className="min-w-0 flex-1">
          {groups.map((group) => (
            <Fragment key={group.family}>
              <li
                style={{ height: LABEL_ROW }}
                className="flex items-center px-3 text-caption text-text-2"
              >
                {t(FAMILY_LABEL_KEY[group.family])}
              </li>
              {group.dests.map((dest) => (
                <li
                  key={dest.id}
                  data-dest={dest.ref}
                  style={{ height: DEST_ROW }}
                  className={cn(
                    'flex items-center gap-2 px-3 text-body',
                    dest.waiting && 'is-wait',
                  )}
                >
                  <span
                    className={cn(
                      'min-w-0 truncate',
                      dest.waiting && 'border-b border-dashed border-text-2',
                    )}
                    title={dest.ref}
                  >
                    {dest.ref}
                  </span>
                  {dest.waiting && (
                    <Clock
                      aria-hidden
                      className="size-4 shrink-0 text-text-2"
                    />
                  )}
                </li>
              ))}
            </Fragment>
          ))}
        </ul>
      </div>
      <div className="flex flex-wrap items-center gap-x-5 gap-y-1 text-caption text-text-2">
        <span className="flex items-center gap-2">
          <span
            aria-hidden
            className="inline-block h-0 w-8 border-t border-text-2"
          />
          {t('legendReached')}
        </span>
        <span className="flex items-center gap-2">
          <span
            aria-hidden
            className="inline-block h-0 w-8 border-t border-dashed border-text-2"
          />
          {t('legendWaiting')}
        </span>
        <span>{t('approvedNote')}</span>
      </div>
    </div>
  )
}
