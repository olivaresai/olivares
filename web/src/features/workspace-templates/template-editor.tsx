// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// TemplateEditor — Sheet-based form for creating or editing workspace templates.
// Sections: Hooks (dynamic list of command entries), Settings (permission_mode,
// effort, model, custom_instructions), Connectors (comma-separated IDs), and
// Policies (dlp_mode, max_session_duration_minutes, allowed_tools, record_io).
// Built-in templates cannot be edited; they render a read-only view.
// Uses controlled state (no external form library dependency required).
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Lock, Plus, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Separator } from '@/components/ui/separator'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Textarea } from '@/components/ui/textarea'
import { toast } from '@/components/ui/toaster'
import { useAuth } from '@/lib/auth/context'
import { templatesApi, templatesKeys } from './api'
import type {
  HookEntry,
  TemplateBody,
  TemplateDTO,
  TemplatePolicies,
  TemplateSettings,
} from './types'
import './i18n'

// ⛔ THE ENGINE'S OWN VOCABULARIES, not a parallel set. Every value here used to be
// invented: the modes were default|restricted|permissive and the DLP modes
// off|log|redact|block, and NONE of restricted, permissive, log, redact or block has ever
// existed in this product (modules/sessions/runtime_ports.go validPermissionModes;
// modules/sessions/workspace_schema.go). xhigh was missing from the effort list.
//
// The editor was therefore an authoring surface whose dropdowns produced templates the
// server refuses at launch — the same "declared and not done" defect as the endpoint this
// pack fixed, entering from the other end. Found by the Codex sol max contrast, 2026-08-11.
const PERMISSION_MODES = [
  'default',
  'acceptEdits',
  'plan',
  'auto',
  'dontAsk',
  'bypassPermissions',
] as const
const EFFORT_LEVELS = ['low', 'medium', 'high', 'xhigh', 'max'] as const
const DLP_MODES = ['off', 'label', 'deny'] as const

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

type HookKind = 'pre_tool' | 'post_tool' | 'pre_session' | 'post_session'

interface FormHook {
  command: string
  timeout_ms: string // stored as string for the input, parsed on submit
}

interface FormState {
  name: string
  description: string
  hooks: Record<HookKind, FormHook[]>
  settings: {
    permission_mode: string
    effort: string
    model: string
    custom_instructions: string
  }
  connectors: string // comma-separated, split on submit
  policies: {
    dlp_mode: string
    max_session_duration_minutes: string
    allowed_tools: string // comma-separated
    record_io: boolean
  }
}

const HOOK_KINDS: HookKind[] = ['pre_tool', 'post_tool', 'pre_session', 'post_session']

function emptyFormState(): FormState {
  return {
    name: '',
    description: '',
    hooks: { pre_tool: [], post_tool: [], pre_session: [], post_session: [] },
    settings: { permission_mode: '', effort: '', model: '', custom_instructions: '' },
    connectors: '',
    policies: { dlp_mode: '', max_session_duration_minutes: '', allowed_tools: '', record_io: false },
  }
}

function dtoToFormState(dto: TemplateDTO): FormState {
  const h = dto.body.hooks ?? {}
  const s = dto.body.settings ?? {}
  const p = dto.body.policies ?? {}
  return {
    name: dto.name,
    description: dto.description,
    hooks: {
      pre_tool: (h.pre_tool ?? []).map(hToForm),
      post_tool: (h.post_tool ?? []).map(hToForm),
      pre_session: (h.pre_session ?? []).map(hToForm),
      post_session: (h.post_session ?? []).map(hToForm),
    },
    settings: {
      permission_mode: s.permission_mode ?? '',
      effort: s.effort ?? '',
      model: s.model ?? '',
      custom_instructions: s.custom_instructions ?? '',
    },
    connectors: (dto.body.connectors ?? []).join(', '),
    policies: {
      dlp_mode: p.dlp_mode ?? '',
      max_session_duration_minutes: p.max_session_duration_minutes != null
        ? String(p.max_session_duration_minutes)
        : '',
      allowed_tools: (p.allowed_tools ?? []).join(', '),
      record_io: p.record_io ?? false,
    },
  }
}

function hToForm(e: HookEntry): FormHook {
  return { command: e.command, timeout_ms: e.timeout_ms != null ? String(e.timeout_ms) : '' }
}

function formStateToBody(f: FormState): TemplateBody {
  const hooks: TemplateBody['hooks'] = {}
  for (const kind of HOOK_KINDS) {
    const entries = f.hooks[kind]
      .filter((e) => e.command.trim())
      .map((e): HookEntry => ({
        command: e.command.trim(),
        ...(e.timeout_ms.trim() ? { timeout_ms: Number(e.timeout_ms) } : {}),
      }))
    if (entries.length) hooks[kind] = entries
  }

  const settings: TemplateSettings = {}
  if (f.settings.permission_mode) settings.permission_mode = f.settings.permission_mode
  if (f.settings.effort) settings.effort = f.settings.effort
  if (f.settings.model.trim()) settings.model = f.settings.model.trim()
  if (f.settings.custom_instructions.trim())
    settings.custom_instructions = f.settings.custom_instructions.trim()

  const connectors = f.connectors
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)

  const policies: TemplatePolicies = {}
  if (f.policies.dlp_mode) policies.dlp_mode = f.policies.dlp_mode
  if (f.policies.max_session_duration_minutes.trim())
    policies.max_session_duration_minutes = Number(f.policies.max_session_duration_minutes)
  const allowedTools = f.policies.allowed_tools
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)
  if (allowedTools.length) policies.allowed_tools = allowedTools
  if (f.policies.record_io) policies.record_io = true

  return {
    ...(Object.keys(hooks).length ? { hooks } : {}),
    ...(Object.keys(settings).length ? { settings } : {}),
    ...(connectors.length ? { connectors } : {}),
    ...(Object.keys(policies).length ? { policies } : {}),
  }
}

// ---------------------------------------------------------------------------
// Main component
// ---------------------------------------------------------------------------

export interface TemplateEditorProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Template to edit; undefined = create mode. */
  template?: TemplateDTO
}

export function TemplateEditor({ open, onOpenChange, template }: TemplateEditorProps) {
  const { t } = useTranslation('workspace-templates')
  const { activeTenant } = useAuth()
  const queryClient = useQueryClient()

  // Reset form state whenever the sheet opens or the template changes.
  const [initKey, setInitKey] = useState(0)
  const [form, setFormRaw] = useState<FormState>(emptyFormState)

  // Re-derive form state when template/open changes (render-time reset).
  const [prevKey, setPrevKey] = useState<string>('')
  const derivedKey = `${String(open)}-${template?.id ?? 'new'}-${template?.updated_at ?? ''}`
  if (derivedKey !== prevKey) {
    setPrevKey(derivedKey)
    setFormRaw(template ? dtoToFormState(template) : emptyFormState())
    setInitKey((k) => k + 1)
  }

  const [nameError, setNameError] = useState('')

  const set = <K extends keyof FormState>(key: K, value: FormState[K]) =>
    setFormRaw((prev) => ({ ...prev, [key]: value }))

  const createMutation = useMutation({
    mutationFn: () =>
      templatesApi.create({ name: form.name.trim(), description: form.description.trim(), body: formStateToBody(form) }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: templatesKeys.all(activeTenant) })
      onOpenChange(false)
      toast.success(t('actions.save'))
    },
    onError: () => toast.error(t('errors.saveFailed')),
  })

  const updateMutation = useMutation({
    mutationFn: () =>
      templatesApi.update(template!.id, {
        name: form.name.trim(),
        description: form.description.trim(),
        body: formStateToBody(form),
      }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: templatesKeys.all(activeTenant) })
      onOpenChange(false)
      toast.success(t('actions.save'))
    },
    onError: () => toast.error(t('errors.saveFailed')),
  })

  const isPending = createMutation.isPending || updateMutation.isPending
  const isBuiltin = template?.builtin ?? false

  function handleSubmit() {
    if (!form.name.trim()) {
      setNameError(t('editor.nameLabel'))
      return
    }
    setNameError('')
    if (template) {
      updateMutation.mutate()
    } else {
      createMutation.mutate()
    }
  }

  // Built-in templates are shown read-only.
  if (isBuiltin) {
    return (
      <Sheet open={open} onOpenChange={onOpenChange}>
        <SheetContent className="w-full overflow-y-auto sm:max-w-lg">
          <SheetHeader>
            <SheetTitle className="flex items-center gap-2">
              <Lock className="size-4 text-muted-foreground" />
              {template?.name}
            </SheetTitle>
            <SheetDescription className="flex items-center gap-1.5">
              <Badge variant="accent">{t('catalog.builtin')}</Badge>
            </SheetDescription>
          </SheetHeader>
          <p className="text-sm text-muted-foreground">{template?.description}</p>
        </SheetContent>
      </Sheet>
    )
  }

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="flex w-full flex-col overflow-y-auto sm:max-w-lg" key={initKey}>
        <SheetHeader>
          <SheetTitle>
            {template ? t('actions.edit') : t('actions.create')}
          </SheetTitle>
          {template && (
            <SheetDescription>
              {t('catalog.version', { version: template.version })}
            </SheetDescription>
          )}
        </SheetHeader>

        <div className="flex flex-1 flex-col gap-6 overflow-y-auto pb-4">
          {/* ── General ── */}
          <section className="flex flex-col gap-4">
            <Field
              label={t('editor.nameLabel')}
              required
              error={nameError || undefined}
            >
              <Input
                value={form.name}
                onChange={(e) => set('name', e.target.value)}
                placeholder={t('editor.namePlaceholder')}
                disabled={isPending}
              />
            </Field>
            <Field label={t('editor.descriptionLabel')}>
              <Textarea
                value={form.description}
                onChange={(e) => set('description', e.target.value)}
                placeholder={t('editor.descriptionPlaceholder')}
                disabled={isPending}
                rows={2}
              />
            </Field>
          </section>

          <Separator />

          {/* ── Hooks ── */}
          <section className="flex flex-col gap-3">
            <div>
              <h3 className="text-sm font-semibold text-foreground">
                {t('editor.sections.hooks')}
              </h3>
              <p className="text-xs text-muted-foreground">
                {t('editor.sections.hooksHint')}
              </p>
            </div>
            {HOOK_KINDS.map((kind) => (
              <HookSection
                key={kind}
                kind={kind}
                entries={form.hooks[kind]}
                disabled={isPending}
                onChange={(entries) =>
                  set('hooks', { ...form.hooks, [kind]: entries })
                }
              />
            ))}
          </section>

          <Separator />

          {/* ── Settings ── */}
          <section className="flex flex-col gap-3">
            <div>
              <h3 className="text-sm font-semibold text-foreground">
                {t('editor.sections.settings')}
              </h3>
              <p className="text-xs text-muted-foreground">
                {t('editor.sections.settingsHint')}
              </p>
            </div>
            <Field label={t('editor.settings.permissionMode')}>
              <Select
                value={form.settings.permission_mode}
                onValueChange={(v) =>
                  set('settings', { ...form.settings, permission_mode: v === '__none__' ? '' : v })
                }
                disabled={isPending}
              >
                <SelectTrigger>
                  <SelectValue placeholder="—" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="__none__">—</SelectItem>
                  {PERMISSION_MODES.map((m) => (
                    <SelectItem key={m} value={m}>
                      {m}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
            <Field label={t('editor.settings.effort')}>
              <Select
                value={form.settings.effort}
                onValueChange={(v) =>
                  set('settings', { ...form.settings, effort: v === '__none__' ? '' : v })
                }
                disabled={isPending}
              >
                <SelectTrigger>
                  <SelectValue placeholder="—" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="__none__">—</SelectItem>
                  {EFFORT_LEVELS.map((e) => (
                    <SelectItem key={e} value={e}>
                      {e}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
            <Field label={t('editor.settings.model')}>
              <Input
                value={form.settings.model}
                onChange={(e) =>
                  set('settings', { ...form.settings, model: e.target.value })
                }
                placeholder="e.g. claude-opus-4-5"
                disabled={isPending}
                mono
              />
            </Field>
            <Field label={t('editor.settings.customInstructions')}>
              <Textarea
                value={form.settings.custom_instructions}
                onChange={(e) =>
                  set('settings', { ...form.settings, custom_instructions: e.target.value })
                }
                rows={4}
                disabled={isPending}
              />
            </Field>
          </section>

          <Separator />

          {/* ── Connectors ── */}
          <section className="flex flex-col gap-3">
            <div>
              <h3 className="text-sm font-semibold text-foreground">
                {t('editor.sections.connectors')}
              </h3>
              <p className="text-xs text-muted-foreground">
                {t('editor.sections.connectorsHint')}
              </p>
            </div>
            <Input
              value={form.connectors}
              onChange={(e) => set('connectors', e.target.value)}
              placeholder="connector-a, connector-b"
              disabled={isPending}
              mono
            />
          </section>

          <Separator />

          {/* ── Policies ── */}
          <section className="flex flex-col gap-3">
            <div>
              <h3 className="text-sm font-semibold text-foreground">
                {t('editor.sections.policies')}
              </h3>
              <p className="text-xs text-muted-foreground">
                {t('editor.sections.policiesHint')}
              </p>
            </div>
            <Field label={t('editor.policies.dlpMode')}>
              <Select
                value={form.policies.dlp_mode}
                onValueChange={(v) =>
                  set('policies', { ...form.policies, dlp_mode: v === '__none__' ? '' : v })
                }
                disabled={isPending}
              >
                <SelectTrigger>
                  <SelectValue placeholder="—" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="__none__">—</SelectItem>
                  {DLP_MODES.map((m) => (
                    <SelectItem key={m} value={m}>
                      {m}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
            <Field label={t('editor.policies.maxDuration')}>
              <Input
                type="number"
                min={1}
                value={form.policies.max_session_duration_minutes}
                onChange={(e) =>
                  set('policies', {
                    ...form.policies,
                    max_session_duration_minutes: e.target.value,
                  })
                }
                placeholder="—"
                disabled={isPending}
              />
            </Field>
            <Field
              label={t('editor.policies.allowedTools')}
              description={t('editor.policies.allowedToolsPlaceholder')}
            >
              <Input
                value={form.policies.allowed_tools}
                onChange={(e) =>
                  set('policies', { ...form.policies, allowed_tools: e.target.value })
                }
                placeholder={t('editor.policies.allowedToolsPlaceholder')}
                disabled={isPending}
                mono
              />
            </Field>
            <div className="flex items-center gap-2">
              <Checkbox
                id="record-io"
                checked={form.policies.record_io}
                onCheckedChange={(checked) =>
                  set('policies', { ...form.policies, record_io: checked === true })
                }
                disabled={isPending}
              />
              <Label htmlFor="record-io" className="cursor-pointer text-sm">
                {t('editor.policies.recordIo')}
              </Label>
            </div>
          </section>
        </div>

        <SheetFooter>
          <Button variant="secondary" onClick={() => onOpenChange(false)} disabled={isPending}>
            {t('actions.cancel')}
          </Button>
          <Button variant="primary" onClick={handleSubmit} disabled={isPending}>
            {t('actions.save')}
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}

// ---------------------------------------------------------------------------
// HookSection — one lifecycle phase (pre_tool, post_tool, …)
// ---------------------------------------------------------------------------

interface HookSectionProps {
  kind: HookKind
  entries: FormHook[]
  disabled: boolean
  onChange: (entries: FormHook[]) => void
}

const HOOK_KIND_LABELS: Record<HookKind, string> = {
  pre_tool: 'hooks.preTool',
  post_tool: 'hooks.postTool',
  pre_session: 'hooks.preSession',
  post_session: 'hooks.postSession',
}

function HookSection({ kind, entries, disabled, onChange }: HookSectionProps) {
  const { t } = useTranslation('workspace-templates')

  function addEntry() {
    onChange([...entries, { command: '', timeout_ms: '' }])
  }

  function removeEntry(idx: number) {
    onChange(entries.filter((_, i) => i !== idx))
  }

  function updateEntry(idx: number, field: keyof FormHook, value: string) {
    onChange(entries.map((e, i) => (i === idx ? { ...e, [field]: value } : e)))
  }

  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center justify-between">
        <span className="text-xs font-medium text-muted-foreground">
          {t(`editor.${HOOK_KIND_LABELS[kind]}`)}
        </span>
        <Button
          variant="ghost"
          size="sm"
          onClick={addEntry}
          disabled={disabled}
          type="button"
        >
          <Plus className="size-3" />
          {t('editor.hooks.addHook')}
        </Button>
      </div>
      {entries.length === 0 ? null : (
        <div className="flex flex-col gap-2">
          {entries.map((entry, idx) => (
            <div key={idx} className="flex items-start gap-2">
              <div className="flex flex-1 flex-col gap-1">
                <Input
                  value={entry.command}
                  onChange={(e) => updateEntry(idx, 'command', e.target.value)}
                  placeholder={t('editor.hooks.command')}
                  disabled={disabled}
                  mono
                  aria-label={t('editor.hooks.command')}
                />
                <Input
                  value={entry.timeout_ms}
                  onChange={(e) => updateEntry(idx, 'timeout_ms', e.target.value)}
                  placeholder={t('editor.hooks.timeoutMs')}
                  type="number"
                  min={0}
                  disabled={disabled}
                  aria-label={t('editor.hooks.timeoutMs')}
                />
              </div>
              <Button
                variant="ghost"
                size="icon-sm"
                onClick={() => removeEntry(idx)}
                disabled={disabled}
                type="button"
                aria-label={t('editor.hooks.removeHook')}
                className="mt-0.5 shrink-0 text-danger hover:bg-danger-soft hover:text-danger"
              >
                <Trash2 className="size-3.5" />
              </Button>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
