// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useId, useState, type FormEvent } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { consoleApi, consoleKeys } from '@/features/console/api'
import { useAuth } from '@/lib/auth/context'
import { useWorkspaceStore } from '@/stores/workspace'

const PRIORITIES = ['p0', 'p1', 'p2', 'p3'] as const

/** What a person fills in. Everything else in the command is known: the owner is the
 * person, the provenance is a human at the console. */
interface NewItemDraft {
  workspaceId: string
  title: string
  brief: string
  doneWhen: string
  priority: string
}

/**
 * The item.create document the engine accepts (modules/sessions/work_state.go,
 * validateCommandSyntax): every field it requires, in its own vocabulary.
 * `owner_ref` is the bare user id, never `user:<id>` or an email; `provenance_kind`
 * is one of its closed set, and a person at the console is `human`. The engine
 * still checks all of it, and the plan step shows its answer before anything is
 * written.
 */
function itemCreateBody(
  draft: NewItemDraft,
  ownerRef: string,
): Record<string, unknown> {
  return {
    workspace_id: draft.workspaceId,
    title: draft.title.trim(),
    work_kind: 'task',
    brief_md: draft.brief.trim(),
    priority: draft.priority,
    owner_kind: 'user',
    owner_ref: ownerRef,
    provenance_kind: 'human',
    provenance_ref: 'console',
    acceptance: [
      {
        criterion_key: 'done',
        statement: draft.doneWhen.trim(),
        required: true,
      },
    ],
  }
}

/**
 * NewItemDialog collects what a person must say about a new work item and hands the
 * command document to the caller, which plans and applies it through ApplyFlow, the
 * same three-phase path every other work change takes. This dialog sends nothing.
 */
export function NewItemDialog({
  open,
  onOpenChange,
  ownerRef,
  onContinue,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** The acting user's id: the owner the engine checks. */
  ownerRef: string
  onContinue: (body: Record<string, unknown>) => void
}) {
  const { t } = useTranslation('work')
  const { activeTenant, can, confinedWorkspace } = useAuth()
  const { activeWorkspace, activeWorkspaceName } = useWorkspaceStore()
  const idp = useId()

  const workspaces = useQuery({
    queryKey: consoleKeys.workspaces(activeTenant),
    queryFn: () => consoleApi.listWorkspaces(),
    enabled: open && can('tenant:read'),
  })
  const items = workspaces.data?.items ?? []

  const [chosen, setChosen] = useState('')
  const [title, setTitle] = useState('')
  const [brief, setBrief] = useState('')
  const [doneWhen, setDoneWhen] = useState('')
  const [priority, setPriority] = useState<string>('p2')

  // A confined principal has exactly one workspace. Otherwise the person's choice, then
  // the topbar's, then the tenant's default, then the only one there is.
  const workspaceId =
    confinedWorkspace ||
    chosen ||
    activeWorkspace ||
    items.find((w) => w.is_default)?.id ||
    (items.length === 1 ? items[0].id : '')
  // A workspace known without the list (confined, or the topbar's while the list is
  // unreadable) is still shown, so the person sees where the item will go.
  const options =
    workspaceId && !items.some((w) => w.id === workspaceId)
      ? [
          ...items,
          {
            id: workspaceId,
            name:
              workspaceId === activeWorkspace && activeWorkspaceName
                ? activeWorkspaceName
                : workspaceId,
            slug: '',
          },
        ]
      : items

  const ready =
    workspaceId !== '' &&
    title.trim() !== '' &&
    brief.trim() !== '' &&
    doneWhen.trim() !== ''

  const reset = () => {
    setChosen('')
    setTitle('')
    setBrief('')
    setDoneWhen('')
    setPriority('p2')
  }

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (!ready) return
    onContinue(
      itemCreateBody(
        { workspaceId, title, brief, doneWhen, priority },
        ownerRef,
      ),
    )
    // The draft is kept: if the engine refuses the plan, New item reopens it as typed.
    // The caller remounts this dialog once the item exists.
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) reset()
        onOpenChange(next)
      }}
    >
      <DialogContent className="max-w-xl">
        <DialogHeader>
          <DialogTitle>{t('create.title')}</DialogTitle>
          <DialogDescription>{t('create.description')}</DialogDescription>
        </DialogHeader>
        <form onSubmit={onSubmit} className="flex flex-col gap-4" noValidate>
          <Field
            label={t('create.workspace')}
            htmlFor={`${idp}-ws`}
            // A failed read is said as one, never as "no workspace". A query that is
            // disabled (no tenant:read) is not loading, so it gets the hint too.
            error={workspaces.isError ? t('create.workspaceError') : undefined}
            description={
              !workspaceId && !workspaces.isLoading && !workspaces.isError
                ? t('create.workspaceNone')
                : undefined
            }
            required
          >
            <Select
              value={workspaceId}
              onValueChange={setChosen}
              disabled={!!confinedWorkspace || options.length === 0}
            >
              <SelectTrigger
                id={`${idp}-ws`}
                aria-label={t('create.workspace')}
              >
                <SelectValue placeholder={t('create.workspace')} />
              </SelectTrigger>
              <SelectContent>
                {options.map((w) => (
                  <SelectItem key={w.id} value={w.id}>
                    {w.name || w.slug}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Field label={t('create.itemTitle')} required>
            <Input
              value={title}
              maxLength={256}
              onChange={(e) => setTitle(e.currentTarget.value)}
            />
          </Field>
          <Field
            label={t('create.brief')}
            description={t('create.briefHint')}
            required
          >
            <Textarea
              value={brief}
              rows={4}
              onChange={(e) => setBrief(e.currentTarget.value)}
            />
          </Field>
          <Field
            label={t('create.doneWhen')}
            description={t('create.doneWhenHint')}
            required
          >
            <Input
              value={doneWhen}
              maxLength={4096}
              onChange={(e) => setDoneWhen(e.currentTarget.value)}
            />
          </Field>
          <Field label={t('filters.priority')} htmlFor={`${idp}-priority`}>
            <Select value={priority} onValueChange={setPriority}>
              <SelectTrigger
                id={`${idp}-priority`}
                className="w-32"
                aria-label={t('filters.priority')}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {PRIORITIES.map((p) => (
                  <SelectItem key={p} value={p}>
                    {p}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <DialogFooter>
            <Button
              type="button"
              variant="ghost"
              onClick={() => {
                reset()
                onOpenChange(false)
              }}
            >
              {t('apply.cancel')}
            </Button>
            <Button type="submit" disabled={!ready}>
              {t('apply.continue')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
