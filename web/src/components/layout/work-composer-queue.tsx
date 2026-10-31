// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { ArrowDown, ArrowUp, Pencil, X } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { useQueueActions } from '@/features/agentops/run-queue'
import type { RunDTO, RunQueueDTO } from '@/features/agentops/types'

/**
 * THE MESSAGES WAITING FOR THE TOOL'S TURN TO END, above the field.
 *
 * Each can be edited, moved, removed or sent now; the line above them says why the first
 * has not gone (the tool is answering, an approval waits, a limit ended the last turn,
 * the session stopped). The list is the engine's, so it is the same after a reload and on
 * another device. While a turn runs the send is Steer (into the turn, through the tool's
 * own input, and only for a tool that takes one); otherwise it is Send now.
 */
export function WorkComposerQueue({
  run,
  queue,
}: {
  run: Pick<RunDTO, 'run_ref'>
  queue: RunQueueDTO | undefined
}) {
  const { t } = useTranslation()
  const actions = useQueueActions(run)
  const [editing, setEditing] = useState<{ id: string; text: string } | null>(
    null,
  )
  if (!queue || queue.items.length === 0) return null

  const ids = queue.items.map((item) => item.id)
  const busy =
    actions.edit.isPending ||
    actions.order.isPending ||
    actions.remove.isPending ||
    actions.send.isPending
  const running = queue.hold === 'turn'
  const move = (index: number, by: -1 | 1) => {
    const next = [...ids]
    ;[next[index], next[index + by]] = [next[index + by], next[index]]
    actions.order.mutate(next)
  }
  const save = (id: string, original: string) => {
    if (!editing || !editing.text.trim()) return
    if (editing.text === original) return setEditing(null)
    actions.edit.mutate(
      { id, text: editing.text },
      { onSuccess: () => setEditing(null) },
    )
  }

  return (
    <section
      aria-label={t('nav:launcher.queue.title')}
      data-testid="composer-queue"
      className="flex flex-col gap-1.5 border-b border-line px-3 py-2"
    >
      <p
        role="status"
        data-testid="composer-queue-hold"
        className="text-caption text-text-2"
      >
        {t(`nav:launcher.queue.hold.${queue.hold ?? 'ready'}`)}
      </p>
      <ol className="flex flex-col gap-1">
        {queue.items.map((item, index) => (
          <li
            key={item.id}
            data-testid="composer-queue-item"
            className="flex items-start gap-2 rounded-md bg-surface px-2 py-1"
          >
            {editing?.id === item.id ? (
              <div className="flex min-w-0 flex-1 flex-col gap-1">
                <textarea
                  autoFocus
                  rows={2}
                  value={editing.text}
                  aria-label={t('nav:launcher.queue.editAria')}
                  onChange={(e) =>
                    setEditing({ id: item.id, text: e.target.value })
                  }
                  onKeyDown={(e) => {
                    if (e.nativeEvent.isComposing) return
                    if (e.key === 'Escape') {
                      e.stopPropagation()
                      setEditing(null)
                    } else if (e.key === 'Enter' && !e.shiftKey) {
                      e.preventDefault()
                      save(item.id, item.text)
                    }
                  }}
                  className="w-full resize-none rounded-md border border-line bg-raised px-2 py-1 text-body text-text outline-none focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus"
                />
                <div className="flex gap-1">
                  <Button
                    type="button"
                    size="sm"
                    disabled={busy || !editing.text.trim()}
                    onClick={() => save(item.id, item.text)}
                  >
                    {t('nav:launcher.queue.save')}
                  </Button>
                  <Button
                    type="button"
                    size="sm"
                    variant="ghost"
                    onClick={() => setEditing(null)}
                  >
                    {t('nav:launcher.queue.cancel')}
                  </Button>
                </div>
              </div>
            ) : (
              <>
                <p className="line-clamp-3 min-w-0 flex-1 whitespace-pre-wrap break-words text-body text-text">
                  {item.text}
                </p>
                <div className="flex shrink-0 items-center">
                  {running && !queue.can_steer ? null : (
                    <Button
                      type="button"
                      size="sm"
                      variant="secondary"
                      disabled={busy}
                      onClick={() => actions.send.mutate(item.id)}
                    >
                      {t(
                        running
                          ? 'nav:launcher.queue.steer'
                          : 'nav:launcher.queue.sendNow',
                      )}
                    </Button>
                  )}
                  <Button
                    type="button"
                    size="icon-sm"
                    variant="ghost"
                    aria-label={t('nav:launcher.queue.moveUp')}
                    disabled={busy || index === 0}
                    onClick={() => move(index, -1)}
                  >
                    <ArrowUp aria-hidden />
                  </Button>
                  <Button
                    type="button"
                    size="icon-sm"
                    variant="ghost"
                    aria-label={t('nav:launcher.queue.moveDown')}
                    disabled={busy || index === ids.length - 1}
                    onClick={() => move(index, 1)}
                  >
                    <ArrowDown aria-hidden />
                  </Button>
                  <Button
                    type="button"
                    size="icon-sm"
                    variant="ghost"
                    aria-label={t('nav:launcher.queue.edit')}
                    disabled={busy}
                    onClick={() => setEditing({ id: item.id, text: item.text })}
                  >
                    <Pencil aria-hidden />
                  </Button>
                  <Button
                    type="button"
                    size="icon-sm"
                    variant="ghost"
                    aria-label={t('nav:launcher.queue.remove')}
                    disabled={busy}
                    onClick={() => actions.remove.mutate(item.id)}
                  >
                    <X aria-hidden />
                  </Button>
                </div>
              </>
            )}
          </li>
        ))}
      </ol>
    </section>
  )
}
