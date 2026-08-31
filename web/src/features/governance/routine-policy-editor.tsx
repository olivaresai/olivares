// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// · plan 3.6 — authoring for routine governance policies.
//
// Two things in here are load-bearing and easy to get wrong:
//
//  1. THE CADENCE FLOOR HAS THREE LEGAL SHAPES, not a range. 0 means "no
//     floor", and anything else must sit in [60, 31622400] (minCadenceFloor /
//     maxCadenceFloor, routines.go:34,:43). A single numeric input with
//     min=60 makes the no-floor setting UNREACHABLE, so the control is a
//     toggle plus a bounded number, and the bounds are imported from types.ts
//     rather than retyped here.
//
//  2. THE TWO LIST FIELDS ARE TRI-STATE. On update the server reads
//     json.RawMessage precisely so "absent", "null" and "[]" stay three
//     different instructions (routines.go:145-156). This editor NEVER sends
//     "absent": the operator sees the current state and what they see is what
//     is written, so an untouched allowlist can never be silently cleared by a
//     serialiser that turned it into null.
import { useEffect, useMemo, useState } from 'react'
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
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { governanceApi, governanceKeys } from './api'
import './i18n'
import {
  ROUTINE_CADENCE_MAX,
  ROUTINE_CADENCE_MIN,
  ROUTINE_SCOPE_KINDS,
  routineListState,
  type RoutineListState,
  type RoutinePolicyDTO,
  type RoutineScopeKind,
} from './types'

/** Split a textarea into entries the way an operator writes them: one per line
 * or comma-separated, whitespace trimmed, blanks dropped. */
function parseEntries(raw: string): string[] {
  return raw
    .split(/[\n,]/)
    .map((entry) => entry.trim())
    .filter((entry) => entry !== '')
}

/**
 * ListEditor is the tri-state control. It exposes the three states as three
 * NAMED choices instead of inferring them from an empty textbox — "I left the
 * box empty" and "I am denying everything" are different intentions and the
 * engine acts on them differently.
 */
function ListEditor({
  kind,
  state,
  entries,
  wasUnreadable,
  error,
  onStateChange,
  onEntriesChange,
  testId,
}: {
  kind: 'cron' | 'envs'
  state: RoutineListState
  entries: string
  /** The stored column arrived unreadable, so "leave it alone" is offered. */
  wasUnreadable: boolean
  error?: string
  onStateChange: (next: RoutineListState) => void
  onEntriesChange: (next: string) => void
  testId: string
}) {
  const { t } = useTranslation('governance')
  return (
    <div className="flex flex-col gap-2">
      <Field label={t(`routines.editor.${kind}Label`)}>
        {({ id, 'aria-labelledby': labelledBy }) => (
          <Select
            value={state}
            onValueChange={(next) => onStateChange(next as RoutineListState)}
          >
            <SelectTrigger
              id={id}
              aria-labelledby={labelledBy}
              data-testid={`${testId}-mode`}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {/* Only offered when the stored column is unreadable, and it is
                  the DEFAULT there: writing anything repairs the column and
                  drops a deny-closed the operator never asked to remove. */}
              {wasUnreadable && (
                <SelectItem value="unreadable">
                  {t('routines.editor.keepUnreadable')}
                </SelectItem>
              )}
              <SelectItem value="unset">
                {t(`routines.list.${kind}.unset`)}
              </SelectItem>
              <SelectItem value="listed">
                {t(`routines.editor.${kind}Listed`)}
              </SelectItem>
              <SelectItem value="empty">
                {t(`routines.list.${kind}.empty`)}
              </SelectItem>
            </SelectContent>
          </Select>
        )}
      </Field>
      {state === 'listed' && (
        <Field
          label={t('routines.editor.entries')}
          description={t('routines.editor.entriesHint')}
          error={error}
        >
          {({ id, 'aria-invalid': ariaInvalid }) => (
            <Textarea
              id={id}
              data-testid={`${testId}-entries`}
              value={entries}
              rows={3}
              aria-invalid={ariaInvalid}
              onChange={(event) => onEntriesChange(event.target.value)}
            />
          )}
        </Field>
      )}
      {state === 'empty' && (
        <p className="text-xs text-warning" data-testid={`${testId}-warning`}>
          {t(`routines.editor.${kind}EmptyWarning`)}
        </p>
      )}
      {state === 'unreadable' && (
        <p className="text-xs text-danger" data-testid={`${testId}-unreadable`}>
          {t('routines.editor.unreadableWarning')}
        </p>
      )}
    </div>
  )
}

export interface RoutinePolicyEditorDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** null = create; a policy = edit its MUTABLE fields (name and scope are
   * immutable server-side, routines.go:354). */
  policy: RoutinePolicyDTO | null
  /** The posture scope currently displayed, so a write invalidates the exact
   * posture entry the operator is looking at. */
  postureScope: { workspace_ref?: string; user_ref?: string }
}

export function RoutinePolicyEditorDialog({
  open,
  onOpenChange,
  policy,
  postureScope,
}: RoutinePolicyEditorDialogProps) {
  const { t } = useTranslation(['governance', 'common'])
  const { activeTenant } = useAuth()
  const isEdit = policy !== null

  const [name, setName] = useState('')
  const [scopeKind, setScopeKind] = useState<RoutineScopeKind>('tenant')
  const [scopeRef, setScopeRef] = useState('')
  const [enabled, setEnabled] = useState(true)
  const [hasFloor, setHasFloor] = useState(false)
  const [floor, setFloor] = useState(String(ROUTINE_CADENCE_MIN))
  const [maxActive, setMaxActive] = useState('0')
  const [requireApproval, setRequireApproval] = useState(false)
  const [cronState, setCronState] = useState<RoutineListState>('unset')
  const [cronEntries, setCronEntries] = useState('')
  const [envState, setEnvState] = useState<RoutineListState>('unset')
  const [envEntries, setEnvEntries] = useState('')

  // Seed from the policy every time the dialog opens on a (possibly different)
  // row: a stale form would write another policy's values under this one's id.
  useEffect(() => {
    if (!open) return
    if (!policy) {
      setName('')
      setScopeKind('tenant')
      setScopeRef('')
      setEnabled(true)
      setHasFloor(false)
      setFloor(String(ROUTINE_CADENCE_MIN))
      setMaxActive('0')
      setRequireApproval(false)
      setCronState('unset')
      setCronEntries('')
      setEnvState('unset')
      setEnvEntries('')
      return
    }
    setName(policy.name)
    setScopeKind((policy.scope_kind as RoutineScopeKind) ?? 'tenant')
    setScopeRef(policy.scope_ref ?? '')
    setEnabled(policy.enabled)
    setHasFloor(policy.max_cadence_seconds !== 0)
    setFloor(
      String(
        policy.max_cadence_seconds === 0
          ? ROUTINE_CADENCE_MIN
          : policy.max_cadence_seconds,
      ),
    )
    setMaxActive(String(policy.max_active_routines))
    setRequireApproval(policy.require_approval)
    const cron = routineListState(
      policy.allowed_cron_patterns,
      policy.allowed_cron_patterns_unreadable,
    )
    setCronState(cron)
    // An unreadable column projects as [], so seeding the textarea from it would
    // offer the operator an empty list as if it were the stored content.
    setCronEntries(
      policy.allowed_cron_patterns_unreadable
        ? ''
        : (policy.allowed_cron_patterns ?? []).join('\n'),
    )
    const envs = routineListState(
      policy.blocked_environments,
      policy.blocked_environments_unreadable,
    )
    setEnvState(envs)
    setEnvEntries(
      policy.blocked_environments_unreadable
        ? ''
        : (policy.blocked_environments ?? []).join('\n'),
    )
  }, [open, policy])

  const cadenceSeconds = hasFloor ? Number(floor) : 0
  const activeCap = Number(maxActive)

  /**
   * Validation mirrors the server's, and cites the SAME rule rather than
   * inventing a stricter one: the engine answers a bad cadence with
   * "max_cadence_seconds must be 0 (no floor), or between 60 and 31622400"
   * (routines.go:263-266, :365-369) and a bad scope with "scope_kind must be
   * one of tenant, workspace, user" (:243-245). The numbers below come from
   * the shared constants, so a translation can never drift them.
   */
  const errors = useMemo(() => {
    const out: Record<string, string> = {}
    if (!isEdit && name.trim() === '') out.name = t('routines.editor.nameRequired')
    if (!isEdit) {
      if (scopeKind === 'tenant' && scopeRef.trim() !== '') {
        out.scopeRef = t('routines.editor.tenantScopeRef')
      }
      if (scopeKind !== 'tenant' && scopeRef.trim() === '') {
        out.scopeRef = t('routines.editor.scopeRefRequired')
      }
    }
    if (hasFloor) {
      // An empty box is not a number: Number('') is 0, which would silently
      // become the legal "no floor" value the toggle is supposed to own.
      if (
        floor.trim() === '' ||
        !Number.isInteger(cadenceSeconds) ||
        cadenceSeconds < ROUTINE_CADENCE_MIN ||
        cadenceSeconds > ROUTINE_CADENCE_MAX
      ) {
        out.floor = t('routines.editor.cadenceRange', {
          min: ROUTINE_CADENCE_MIN,
          max: ROUTINE_CADENCE_MAX,
        })
      }
    }
    // Same trap on the cap, and here the silent value REMOVES a control:
    // Number('') is 0, and 0 means "no cap".
    if (maxActive.trim() === '' || !Number.isInteger(activeCap) || activeCap < 0) {
      out.maxActive = t('routines.editor.capRange')
    }
    // "Only these patterns" with nothing listed is a deny-all the operator did
    // not choose — the empty-list mode exists precisely so that choice is named.
    // Two messages, not one: an empty ALLOWLIST denies every cron, an empty
    // BLOCKLIST blocks nothing. A single shared string told the operator the
    // wrong thing about one of the two controls, in all seven languages.
    if (cronState === 'listed' && parseEntries(cronEntries).length === 0) {
      out.cronEntries = t('routines.editor.cronEntriesRequired')
    }
    if (envState === 'listed' && parseEntries(envEntries).length === 0) {
      out.envEntries = t('routines.editor.envsEntriesRequired')
    }
    return out
  }, [
    isEdit,
    name,
    scopeKind,
    scopeRef,
    hasFloor,
    floor,
    maxActive,
    cadenceSeconds,
    activeCap,
    cronState,
    cronEntries,
    envState,
    envEntries,
    t,
  ])

  const invalid = Object.keys(errors).length > 0

  /**
   * The write value for one list, or `undefined` for "do not send this field at
   * all". Undefined is reserved for a column the engine reported as unreadable:
   * the PUT decoder treats an absent key as "leave it alone"
   * (applyJSONListUpdate), which is the only instruction that does not repair
   * the column behind the operator's back.
   */
  function listValue(
    state: RoutineListState,
    raw: string,
  ): string[] | null | undefined {
    if (state === 'unreadable') return undefined
    if (state === 'unset') return null
    if (state === 'empty') return []
    return parseEntries(raw)
  }

  const save = usePrivilegedMutation({
    mutationFn: () => {
      const cron = listValue(cronState, cronEntries)
      const envs = listValue(envState, envEntries)
      if (policy) {
        return governanceApi.updateRoutinePolicy(policy.id, {
          enabled,
          max_cadence_seconds: cadenceSeconds,
          max_active_routines: activeCap,
          require_approval: requireApproval,
          // Explicit on every save: null CLEARS back to any/none and [] is an
          // authored deny-all. Both are intentions the operator just saw. The
          // key is OMITTED only for a column reported unreadable, so an edit of
          // the cadence floor cannot repair it into a permissive empty list.
          ...(cron === undefined ? {} : { allowed_cron_patterns: cron }),
          ...(envs === undefined ? {} : { blocked_environments: envs }),
        })
      }
      return governanceApi.createRoutinePolicy({
        name: name.trim(),
        scope_kind: scopeKind,
        ...(scopeKind === 'tenant' ? {} : { scope_ref: scopeRef.trim() }),
        enabled,
        max_cadence_seconds: cadenceSeconds,
        max_active_routines: activeCap,
        require_approval: requireApproval,
        // On CREATE the key is omitted for "any/none": an absent key and an
        // explicit null both leave the column unset, while `[]` decodes to a
        // NON-NIL empty slice and is stored as the authored deny-all.
        ...(cron === null || cron === undefined
          ? {}
          : { allowed_cron_patterns: cron }),
        ...(envs === null || envs === undefined
          ? {}
          : { blocked_environments: envs }),
      })
    },
    invalidateKeys: () => [
      governanceKeys.routinePolicies(activeTenant),
      governanceKeys.routinePosture(activeTenant, postureScope),
    ],
    successMessage: isEdit
      ? t('routines.editor.updated')
      : t('routines.editor.created'),
    onDone: () => onOpenChange(false),
  })

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next && save.isPending) return
        onOpenChange(next)
      }}
    >
      <DialogContent className="max-h-[85vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>
            {isEdit ? t('routines.editor.editTitle') : t('routines.editor.newTitle')}
          </DialogTitle>
          <DialogDescription>
            {isEdit
              ? t('routines.editor.editDescription')
              : t('routines.editor.newDescription')}
          </DialogDescription>
        </DialogHeader>

        <form
          className="flex flex-col gap-4"
          onSubmit={(event) => {
            event.preventDefault()
            if (!invalid) save.mutate(undefined)
          }}
        >
          <Field
            label={t('routines.editor.name')}
            required={!isEdit}
            error={errors.name}
            description={isEdit ? t('routines.editor.nameImmutable') : undefined}
          >
            {({ id, 'aria-invalid': ariaInvalid }) => (
              <Input
                id={id}
                data-testid="routine-editor-name"
                value={name}
                disabled={isEdit}
                aria-invalid={ariaInvalid}
                onChange={(event) => setName(event.target.value)}
              />
            )}
          </Field>

          <Field
            label={t('routines.editor.scopeKind')}
            description={isEdit ? t('routines.editor.scopeImmutable') : undefined}
          >
            {({ id, 'aria-labelledby': labelledBy }) => (
              <Select
                value={scopeKind}
                disabled={isEdit}
                onValueChange={(next) => {
                  setScopeKind(next as RoutineScopeKind)
                  if (next === 'tenant') setScopeRef('')
                }}
              >
                <SelectTrigger
                  id={id}
                  aria-labelledby={labelledBy}
                  data-testid="routine-editor-scope-kind"
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {ROUTINE_SCOPE_KINDS.map((kind) => (
                    <SelectItem key={kind} value={kind}>
                      {t(`routines.scope.${kind}`)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
          </Field>

          {scopeKind !== 'tenant' && (
            <Field
              label={t('routines.editor.scopeRef')}
              required={!isEdit}
              error={errors.scopeRef}
            >
              {({ id, 'aria-invalid': ariaInvalid }) => (
                <Input
                  id={id}
                  data-testid="routine-editor-scope-ref"
                  value={scopeRef}
                  disabled={isEdit}
                  aria-invalid={ariaInvalid}
                  onChange={(event) => setScopeRef(event.target.value)}
                />
              )}
            </Field>
          )}

          <div className="flex items-center justify-between gap-4 rounded-md border border-border p-3">
            <span className="text-sm font-medium text-foreground">
              {t('routines.editor.enabled')}
            </span>
            <Switch
              checked={enabled}
              onCheckedChange={setEnabled}
              data-testid="routine-editor-enabled"
              aria-label={t('routines.editor.enabled')}
            />
          </div>

          {/* 0 is a legal, meaningful value — not "the field is empty". */}
          <div className="flex flex-col gap-2 rounded-md border border-border p-3">
            <div className="flex items-center justify-between gap-4">
              <span className="text-sm font-medium text-foreground">
                {t('routines.editor.floorToggle')}
              </span>
              <Switch
                checked={hasFloor}
                onCheckedChange={setHasFloor}
                data-testid="routine-editor-has-floor"
                aria-label={t('routines.editor.floorToggle')}
              />
            </div>
            {hasFloor ? (
              <Field
                label={t('routines.editor.floor')}
                error={errors.floor}
                description={t('routines.editor.floorHint', {
                  min: ROUTINE_CADENCE_MIN,
                  max: ROUTINE_CADENCE_MAX,
                })}
              >
                {({ id, 'aria-invalid': ariaInvalid }) => (
                  <Input
                    id={id}
                    type="number"
                    inputMode="numeric"
                    min={ROUTINE_CADENCE_MIN}
                    max={ROUTINE_CADENCE_MAX}
                    data-testid="routine-editor-floor"
                    value={floor}
                    aria-invalid={ariaInvalid}
                    onChange={(event) => setFloor(event.target.value)}
                  />
                )}
              </Field>
            ) : (
              <p className="text-xs text-muted-foreground">
                {t('routines.editor.noFloorHint')}
              </p>
            )}
          </div>

          <Field
            label={t('routines.editor.maxActive')}
            error={errors.maxActive}
            description={t('routines.editor.maxActiveHint')}
          >
            {({ id, 'aria-invalid': ariaInvalid }) => (
              <Input
                id={id}
                type="number"
                inputMode="numeric"
                min={0}
                data-testid="routine-editor-max-active"
                value={maxActive}
                aria-invalid={ariaInvalid}
                onChange={(event) => setMaxActive(event.target.value)}
              />
            )}
          </Field>

          <div className="flex items-center justify-between gap-4 rounded-md border border-border p-3">
            <span className="text-sm font-medium text-foreground">
              {t('routines.editor.requireApproval')}
            </span>
            <Switch
              checked={requireApproval}
              onCheckedChange={setRequireApproval}
              data-testid="routine-editor-approval"
              aria-label={t('routines.editor.requireApproval')}
            />
          </div>

          <ListEditor
            kind="cron"
            state={cronState}
            entries={cronEntries}
            wasUnreadable={policy?.allowed_cron_patterns_unreadable ?? false}
            error={errors.cronEntries}
            onStateChange={setCronState}
            onEntriesChange={setCronEntries}
            testId="routine-editor-cron"
          />
          <ListEditor
            kind="envs"
            state={envState}
            entries={envEntries}
            wasUnreadable={policy?.blocked_environments_unreadable ?? false}
            error={errors.envEntries}
            onStateChange={setEnvState}
            onEntriesChange={setEnvEntries}
            testId="routine-editor-envs"
          />

          <DialogFooter>
            <Button
              type="button"
              variant="ghost"
              onClick={() => onOpenChange(false)}
              disabled={save.isPending}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button
              type="submit"
              variant="primary"
              data-testid="routine-editor-save"
              disabled={invalid || save.isPending}
            >
              {save.isPending && <Spinner size="sm" aria-hidden />}
              {t('common:actions.save')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
