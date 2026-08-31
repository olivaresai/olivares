// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { AlertTriangle } from 'lucide-react'
import { useEffect, useMemo, useState, type FormEvent } from 'react'
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
import { Spinner } from '@/components/ui/spinner'
import { toast } from '@/components/ui/toaster'
import { useAuth } from '@/lib/auth/context'
import { ApiError } from '@/lib/api/errors'
import { templatesApi, templatesKeys } from '@/features/workspace-templates/api'
import { ListTruncationBadge } from '@/features/_intel'
import { agentOpsApi, agentOpsKeys } from './api'
import {
  CRITICAL_PERMISSION_MODES,
  EFFORT_LEVELS,
  PERMISSION_MODES,
  type CreateRunRequest,
  type PermissionMode,
  type Transport,
} from './types'
import './i18n'

const NONE = '__none__'
const DEFAULT_EFFORT = '__default__'

/**
 * RunCreateDialog — the visual equivalent of the CLI launch (name / transport /
 * permission-mode / effort / model / workspace), with the governance posture made
 * explicit BEFORE launch: a privileged permission mode (or a read-write classified
 * workspace) warns that the launch needs human approval and is recorded. It surfaces
 * the backend's honest denial (402/403/429 — budget cap, pending approval) verbatim
 * rather than a generic error.
 */
export function RunCreateDialog({
  open,
  onOpenChange,
  initialTemplateId,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  /** Pre-select a workspace template (the templates catalog launches through here, so
   * "Apply to session" means the session actually comes up under the template). */
  initialTemplateId?: string
}) {
  const { t } = useTranslation('agentops')
  const { activeTenant, can } = useAuth()
  const qc = useQueryClient()

  const [name, setName] = useState('')
  const [transport, setTransport] = useState<Transport>('stream-json')
  const [permissionMode, setPermissionMode] =
    useState<PermissionMode>('default')
  const [effort, setEffort] = useState<string>(DEFAULT_EFFORT)
  const [model, setModel] = useState('')
  const [workspaceRef, setWorkspaceRef] = useState<string>(NONE)
  const [envAllow, setEnvAllow] = useState('')
  const [templateId, setTemplateId] = useState<string>(
    initialTemplateId ?? NONE,
  )

  // Follow the caller's pre-selection when the dialog is (re)opened from a template card.
  useEffect(() => {
    if (open) setTemplateId(initialTemplateId ?? NONE)
  }, [open, initialTemplateId])

  const canReadWs = can('sessions:workspace:read')
  const wsQuery = useQuery({
    queryKey: agentOpsKeys.workspaces(activeTenant),
    queryFn: () => agentOpsApi.listWorkspaces({ limit: 200 }),
    enabled: open && canReadWs,
  })
  const workspaces = useMemo(
    () => (wsQuery.data?.items ?? []).filter((w) => w.state === 'active'),
    [wsQuery.data],
  )
  const selectedWs = workspaces.find((w) => w.workspace_ref === workspaceRef)

  const canReadTemplates = can('sessions:template:read')
  const tplQuery = useQuery({
    queryKey: templatesKeys.list(activeTenant, { launch: true }),
    queryFn: () => templatesApi.list(),
    enabled: open && canReadTemplates,
  })
  const templates = useMemo(
    () => (tplQuery.data?.items ?? []).filter((tpl) => !tpl.archived_at),
    [tplQuery.data],
  )

  // The merge PREVIEW, from the engine — never recomputed in the browser. The whole
  // point of the pack is that the server owns this merge; a second implementation here
  // would be a second answer, and the one the launch uses is the server's.
  const preview = useQuery({
    // Every input the preview depends on is in the key. workspace_ref was missing, so
    // switching workspaces showed the previous target's answer (Codex contrast, 2026-08-11).
    queryKey: [
      ...templatesKeys.detail(activeTenant, templateId),
      'apply',
      transport,
      permissionMode,
      effort,
      model,
      workspaceRef,
    ],
    queryFn: () =>
      templatesApi.apply(templateId, {
        transport,
        permission_mode: permissionMode,
        effort: effort === DEFAULT_EFFORT ? undefined : effort,
        model: model.trim() || undefined,
        workspace_ref: workspaceRef === NONE ? undefined : workspaceRef,
      }),
    enabled: open && templateId !== NONE && canReadTemplates,
  })
  const previewData = templateId === NONE ? undefined : preview.data

  // The warning has to describe the mode the session will actually run in. A template
  // with a tool allow-list pins dontAsk — a CRITICAL launch that needs human approval and
  // is recorded — and reading the raw form field here showed "default" with no warning at
  // all for exactly that launch (Codex contrast, 2026-08-11).
  const effectiveMode = (previewData?.merged?.permission_mode ??
    permissionMode) as PermissionMode
  const isCriticalMode = CRITICAL_PERMISSION_MODES.includes(effectiveMode)
  const isClassifiedRw =
    !!selectedWs &&
    selectedWs.dlp_mode !== 'off' &&
    selectedWs.mount_mode === 'rw'

  const reset = () => {
    setName('')
    setTransport('stream-json')
    setPermissionMode('default')
    setEffort(DEFAULT_EFFORT)
    setModel('')
    setWorkspaceRef(NONE)
    setEnvAllow('')
    setTemplateId(NONE)
  }

  const create = useMutation({
    mutationFn: () => {
      const body: CreateRunRequest = {
        name: name.trim(),
        transport,
        permission_mode: permissionMode,
        effort: effort === DEFAULT_EFFORT ? '' : effort,
        model: model.trim(),
        workspace_ref: workspaceRef === NONE ? '' : workspaceRef,
        isolation: 'native',
        env_allow: envAllow
          .split(',')
          .map((s) => s.trim())
          .filter(Boolean),
        ...(templateId === NONE ? {} : { template_id: templateId }),
      }
      return agentOpsApi.createRun(body)
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: agentOpsKeys.all(activeTenant) })
      toast.success(t('create.success'))
      reset()
      onOpenChange(false)
    },
    onError: (err) => {
      // Surface the backend's honest message: budget cap (402/429) or a CRITICAL
      // launch pending human approval (403 with the approval ref) — never a generic.
      toast.error(err instanceof ApiError ? err.message : t('create.title'))
    },
  })

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (!create.isPending) create.mutate()
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => (create.isPending ? undefined : onOpenChange(o))}
    >
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{t('create.title')}</DialogTitle>
          <DialogDescription>{t('create.description')}</DialogDescription>
        </DialogHeader>

        <form onSubmit={onSubmit} className="flex flex-col gap-3">
          <Field label={t('create.name')}>
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={t('create.namePlaceholder')}
            />
          </Field>

          <div className="grid grid-cols-2 gap-3">
            <Field label={t('create.transport')}>
              <Select
                value={transport}
                onValueChange={(v) => setTransport(v as Transport)}
              >
                <SelectTrigger aria-label={t('create.transport')}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="stream-json">
                    {t('transport.stream-json')}
                  </SelectItem>
                  <SelectItem value="remote-control">
                    {t('transport.remote-control')}
                  </SelectItem>
                </SelectContent>
              </Select>
            </Field>

            <Field label={t('create.permissionMode')}>
              <Select
                value={permissionMode}
                onValueChange={(v) => setPermissionMode(v as PermissionMode)}
              >
                <SelectTrigger aria-label={t('create.permissionMode')}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {PERMISSION_MODES.map((m) => (
                    <SelectItem key={m} value={m}>
                      {t(`permissionMode.${m}`)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          </div>

          <div className="grid grid-cols-2 gap-3">
            <Field label={t('create.effort')}>
              <Select value={effort} onValueChange={setEffort}>
                <SelectTrigger aria-label={t('create.effort')}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={DEFAULT_EFFORT}>
                    {t('create.effortDefault')}
                  </SelectItem>
                  {EFFORT_LEVELS.map((e) => (
                    <SelectItem key={e} value={e}>
                      {t(`effort.${e}`)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>

            <Field label={t('create.model')}>
              <Input
                value={model}
                onChange={(e) => setModel(e.target.value)}
                placeholder={t('create.modelPlaceholder')}
                mono
              />
            </Field>
          </div>

          <Field label={t('create.workspace')}>
            <Select value={workspaceRef} onValueChange={setWorkspaceRef}>
              <SelectTrigger aria-label={t('create.workspace')}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={NONE}>
                  {t('create.workspaceNone')}
                </SelectItem>
                {workspaces.map((w) => (
                  <SelectItem key={w.workspace_ref} value={w.workspace_ref}>
                    {w.name || w.workspace_ref} (
                    {t(`workspaces.mount.${w.mount_mode}`, {
                      defaultValue: w.mount_mode,
                    })}
                    )
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>

          {canReadTemplates && (
            <div className="flex flex-col gap-2">
              <ListTruncationBadge
                query={tplQuery}
                label={t('listTruncation.label', {
                  n: tplQuery.data?.items?.length,
                })}
                hint={t('listTruncation.hint')}
                className="px-0 pt-0"
              />
              <Field
                label={t('create.template')}
                description={t('create.templateHint')}
              >
                <Select value={templateId} onValueChange={setTemplateId}>
                  <SelectTrigger aria-label={t('create.template')}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value={NONE}>
                      {t('create.templateNone')}
                    </SelectItem>
                    {templates.map((tpl) => (
                      <SelectItem key={tpl.id} value={tpl.id}>
                        {tpl.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
            </div>
          )}

          {/* The engine's own verdict on this template, before the launch. A template
              that declares something the launch cannot keep REFUSES the launch — so it
              is shown here rather than as a surprise 422 after pressing Launch. */}
          {previewData && !previewData.applied && (
            <div className="flex items-start gap-2 rounded-md border border-danger-line bg-danger-soft px-2.5 py-2 text-xs text-danger">
              <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
              <div className="min-w-0">
                <p>{t('create.templateUnenforceable')}</p>
                <ul className="mt-1 list-disc space-y-0.5 pl-4">
                  {(previewData.unenforceable ?? []).map((reason) => (
                    <li key={reason} className="break-words">
                      {reason}
                    </li>
                  ))}
                </ul>
              </div>
            </div>
          )}
          {previewData?.applied && previewData.conflicts.length > 0 && (
            <div className="flex items-start gap-2 rounded-md border border-warning-line bg-warning-soft px-2.5 py-2 text-xs text-warning">
              <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
              <div className="min-w-0">
                <p>
                  {t('create.templateOverrides', {
                    count: previewData.conflicts.length,
                  })}
                </p>
                <ul className="mt-1 list-disc space-y-0.5 pl-4">
                  {previewData.conflicts.map((c) => (
                    <li key={c.field} className="break-words">
                      <span className="font-mono">{c.field}</span>:{' '}
                      {String(c.old_value)} →{' '}
                      <span className="font-medium">{String(c.new_value)}</span>
                    </li>
                  ))}
                </ul>
              </div>
            </div>
          )}

          <Field
            label={t('create.envAllow')}
            description={t('create.envAllowHint')}
          >
            <Input
              value={envAllow}
              onChange={(e) => setEnvAllow(e.target.value)}
              placeholder={t('create.envAllowPlaceholder')}
              mono
            />
          </Field>

          <p className="text-xs text-muted-foreground">
            {t('create.isolationNativeOnly')}
          </p>

          {(isCriticalMode || isClassifiedRw) && (
            <div className="flex items-start gap-2 rounded-md border border-warning-line bg-warning-soft px-2.5 py-2 text-xs text-warning">
              <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
              <span>
                {isCriticalMode
                  ? t('create.criticalWarning')
                  : t('create.classifiedWarning')}
              </span>
            </div>
          )}

          <DialogFooter>
            <Button
              type="button"
              variant="secondary"
              onClick={() => onOpenChange(false)}
              disabled={create.isPending}
            >
              {t('browser.cancel')}
            </Button>
            <Button
              type="submit"
              variant="primary"
              disabled={create.isPending || previewData?.applied === false}
            >
              {create.isPending && <Spinner className="size-3.5" />}
              {create.isPending ? t('create.submitting') : t('create.submit')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
