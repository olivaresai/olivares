// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Plus, ScrollText, Trash2 } from 'lucide-react'
import { useState } from 'react'
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
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Spinner } from '@/components/ui/spinner'
import { Switch } from '@/components/ui/switch'
import { useAuth } from '@/lib/auth/context'
import { looksLikeCredential } from '@/lib/credentials'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { governanceApi, governanceKeys } from './api'
import './i18n'
import { ABAC_PRINCIPAL_KINDS, ABAC_VERBS, POLICY_KINDS } from './types'
import type {
  AbacRule,
  AbacSpec,
  ApprovalSpec,
  PolicyDTO,
  PolicyInput,
  PolicyKind,
} from './types'

interface DraftRule extends AbacRule {
  /** Stable client key so React keys survive reordering/removal. */
  _k: string
}

let ruleKeySeq = 0
function newRule(): DraftRule {
  ruleKeySeq += 1
  return {
    _k: `r${ruleKeySeq}`,
    deny: true,
    permission: '',
    verb: '',
    resource: '',
    principal_kind: '',
  }
}

export interface PolicyEditorDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Existing policy to edit; omit/undefined to create. */
  policy?: PolicyDTO | null
}

/**
 * PolicyEditorDialog is the privileged create/edit form for a governance policy. The
 * form itself is the confirmation surface: it carries the audit-ledger notice and a
 * deliberate submit, then runs the privileged mutation (invalidate → toast → close).
 * It surfaces a TYPED form per kind (abac deny rules vs. approval thresholds). The
 * `kind` is immutable on edit. A policy spec never carries a secret value — the
 * inline-credential guard warns if a rule selector looks like one.
 *
 * The form lives in a child that mounts fresh each time the dialog opens (Radix
 * unmounts closed content), so its initial state is seeded from props with plain
 * useState initializers — no resetting effect.
 */
export function PolicyEditorDialog({
  open,
  onOpenChange,
  policy,
}: PolicyEditorDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] max-w-2xl overflow-y-auto">
        {open && (
          <PolicyForm
            policy={policy ?? null}
            onClose={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function asAbacSpec(spec: unknown): AbacSpec {
  const rules = (spec as AbacSpec | null)?.rules
  return { rules: Array.isArray(rules) ? rules : [] }
}

function asApprovalSpec(spec: unknown): ApprovalSpec {
  const s = (spec as ApprovalSpec | null) ?? {}
  return {
    required_approvals: s.required_approvals ?? 1,
    expires_in_seconds: s.expires_in_seconds ?? 0,
    escalate_in_seconds: s.escalate_in_seconds ?? 0,
    match: {
      action: s.match?.action ?? '',
      subject_kind: s.match?.subject_kind ?? '',
    },
  }
}

function PolicyForm({
  policy,
  onClose,
}: {
  policy: PolicyDTO | null
  onClose: () => void
}) {
  const { t } = useTranslation(['governance', 'common'])
  const { activeTenant } = useAuth()
  const isEdit = !!policy?.id

  const [name, setName] = useState(policy?.name ?? '')
  const [kind, setKind] = useState<PolicyKind>(policy?.kind ?? 'abac')
  const [enabled, setEnabled] = useState(policy?.enabled ?? true)

  // ABAC draft rules.
  const [rules, setRules] = useState<DraftRule[]>(() =>
    asAbacSpec(policy?.spec).rules.map((r) => ({
      ...r,
      deny: true,
      _k: `e${ruleKeySeq++}`,
    })),
  )

  // Approval draft.
  const approvalSeed = asApprovalSpec(policy?.spec)
  const [requiredApprovals, setRequiredApprovals] = useState(
    String(approvalSeed.required_approvals ?? 1),
  )
  const [expiresIn, setExpiresIn] = useState(
    String(approvalSeed.expires_in_seconds ?? 0),
  )
  const [escalateIn, setEscalateIn] = useState(
    String(approvalSeed.escalate_in_seconds ?? 0),
  )
  const [matchAction, setMatchAction] = useState(
    approvalSeed.match?.action ?? '',
  )
  const [matchSubjectKind, setMatchSubjectKind] = useState(
    approvalSeed.match?.subject_kind ?? '',
  )

  const ruleWarn = rules.some(
    (r) => looksLikeCredential(r.permission) || looksLikeCredential(r.resource),
  )
  const rulesValid =
    kind !== 'abac' ||
    (rules.length > 0 &&
      rules.every(
        (r) =>
          (r.permission?.trim() ||
            r.verb?.trim() ||
            r.resource?.trim() ||
            r.principal_kind?.trim()) &&
          true,
      ))

  const valid = name.trim().length > 0 && !ruleWarn && rulesValid

  const mutation = usePrivilegedMutation<PolicyInput, PolicyDTO>({
    mutationFn: (input) =>
      isEdit
        ? governanceApi.updatePolicy(policy!.id!, input)
        : governanceApi.createPolicy(input),
    invalidateKeys: () => [
      governanceKeys.policies(activeTenant),
      ...(isEdit ? [governanceKeys.policy(activeTenant, policy!.id!)] : []),
    ],
    successMessage: isEdit
      ? t('policyEditor.updated')
      : t('policyEditor.created'),
    onDone: onClose,
  })

  function buildSpec(): AbacSpec | ApprovalSpec {
    if (kind === 'abac') {
      return {
        rules: rules.map((r) => ({
          deny: true,
          ...(r.permission?.trim() ? { permission: r.permission.trim() } : {}),
          ...(r.verb?.trim() ? { verb: r.verb.trim() } : {}),
          ...(r.resource?.trim() ? { resource: r.resource.trim() } : {}),
          ...(r.principal_kind?.trim()
            ? { principal_kind: r.principal_kind.trim() }
            : {}),
        })),
      }
    }
    const match: ApprovalSpec['match'] = {
      ...(matchAction.trim() ? { action: matchAction.trim() } : {}),
      ...(matchSubjectKind.trim()
        ? { subject_kind: matchSubjectKind.trim() }
        : {}),
    }
    return {
      required_approvals: Number(requiredApprovals) || 0,
      expires_in_seconds: Number(expiresIn) || 0,
      escalate_in_seconds: Number(escalateIn) || 0,
      ...(match.action || match.subject_kind ? { match } : {}),
    }
  }

  function submit() {
    if (!valid) return
    const payload: PolicyInput = {
      name: name.trim(),
      kind,
      enabled,
      spec: buildSpec(),
    }
    mutation.mutate(payload)
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>
          {isEdit ? t('policyEditor.editTitle') : t('policyEditor.createTitle')}
        </DialogTitle>
        <DialogDescription>
          {isEdit
            ? t('policyEditor.confirmUpdateBody')
            : t('policyEditor.confirmCreateBody')}
        </DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <Field
            label={t('policyEditor.name')}
            htmlFor="pol-name"
            description={t('policyEditor.nameHint')}
            required
          >
            <Input
              id="pol-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </Field>
          <Field
            label={t('policyEditor.kind')}
            htmlFor="pol-kind"
            description={t('policyEditor.kindHint')}
          >
            <Select
              value={kind}
              onValueChange={(v) => setKind(v as PolicyKind)}
              disabled={isEdit}
            >
              <SelectTrigger id="pol-kind">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {POLICY_KINDS.map((k) => (
                  <SelectItem key={k} value={k}>
                    {k === 'abac'
                      ? t('policies.kindAbac')
                      : t('policies.kindApproval')}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
        </div>

        <div className="flex items-center gap-2">
          <Switch
            id="pol-enabled"
            checked={enabled}
            onCheckedChange={setEnabled}
          />
          <Label htmlFor="pol-enabled">{t('policyEditor.enabled')}</Label>
        </div>

        {kind === 'abac' ? (
          <div className="flex flex-col gap-2">
            <div className="flex items-center justify-between">
              <Label>{t('policyEditor.abacRules')}</Label>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={() => setRules((r) => [...r, newRule()])}
              >
                <Plus />
                {t('policyEditor.addRule')}
              </Button>
            </div>
            <p className="text-xs text-muted-foreground">
              {t('policyEditor.abacRulesHint')}
            </p>
            {rules.length === 0 ? (
              <p className="rounded-md border border-dashed border-border px-3 py-2 text-xs text-muted-foreground">
                {t('policyEditor.noRules')}
              </p>
            ) : (
              <div className="flex flex-col gap-3">
                {rules.map((r, i) => {
                  const warn =
                    looksLikeCredential(r.permission) ||
                    looksLikeCredential(r.resource)
                  return (
                    <div
                      key={r._k}
                      className="flex flex-col gap-2 rounded-md border border-border bg-muted/40 p-2"
                    >
                      <div className="flex items-center justify-between">
                        <span className="text-xs font-medium text-danger">
                          {t('policyEditor.denyImplicit')}
                        </span>
                        <Button
                          type="button"
                          variant="ghost"
                          size="icon"
                          aria-label={t('policyEditor.removeRule')}
                          onClick={() =>
                            setRules((arr) => arr.filter((_, j) => j !== i))
                          }
                        >
                          <Trash2 />
                        </Button>
                      </div>
                      <Input
                        aria-label={t('policyEditor.rulePermission')}
                        placeholder={t(
                          'policyEditor.rulePermissionPlaceholder',
                        )}
                        value={r.permission ?? ''}
                        onChange={(e) =>
                          setRules((arr) =>
                            arr.map((x, j) =>
                              j === i
                                ? { ...x, permission: e.target.value }
                                : x,
                            ),
                          )
                        }
                        aria-invalid={
                          looksLikeCredential(r.permission) || undefined
                        }
                        mono
                      />
                      <div className="grid grid-cols-1 gap-2 sm:grid-cols-3">
                        <Select
                          value={r.verb || '__any__'}
                          onValueChange={(v) =>
                            setRules((arr) =>
                              arr.map((x, j) =>
                                j === i
                                  ? { ...x, verb: v === '__any__' ? '' : v }
                                  : x,
                              ),
                            )
                          }
                        >
                          <SelectTrigger
                            aria-label={t('policyEditor.ruleVerb')}
                          >
                            <SelectValue
                              placeholder={t('policyEditor.ruleVerb')}
                            />
                          </SelectTrigger>
                          <SelectContent>
                            <SelectItem value="__any__">
                              {t('policyEditor.ruleAny')}
                            </SelectItem>
                            {ABAC_VERBS.map((v) => (
                              <SelectItem key={v} value={v}>
                                {v}
                              </SelectItem>
                            ))}
                          </SelectContent>
                        </Select>
                        <Input
                          aria-label={t('policyEditor.ruleResource')}
                          placeholder={t('policyEditor.ruleResource')}
                          value={r.resource ?? ''}
                          onChange={(e) =>
                            setRules((arr) =>
                              arr.map((x, j) =>
                                j === i
                                  ? { ...x, resource: e.target.value }
                                  : x,
                              ),
                            )
                          }
                          aria-invalid={
                            looksLikeCredential(r.resource) || undefined
                          }
                          mono
                        />
                        <Select
                          value={r.principal_kind || '__any__'}
                          onValueChange={(v) =>
                            setRules((arr) =>
                              arr.map((x, j) =>
                                j === i
                                  ? {
                                      ...x,
                                      principal_kind: v === '__any__' ? '' : v,
                                    }
                                  : x,
                              ),
                            )
                          }
                        >
                          <SelectTrigger
                            aria-label={t('policyEditor.rulePrincipalKind')}
                          >
                            <SelectValue
                              placeholder={t('policyEditor.rulePrincipalKind')}
                            />
                          </SelectTrigger>
                          <SelectContent>
                            <SelectItem value="__any__">
                              {t('policyEditor.ruleAny')}
                            </SelectItem>
                            {ABAC_PRINCIPAL_KINDS.map((p) => (
                              <SelectItem key={p} value={p}>
                                {p}
                              </SelectItem>
                            ))}
                          </SelectContent>
                        </Select>
                      </div>
                      {warn && (
                        <p role="alert" className="text-xs text-danger">
                          {t('policyEditor.credentialWarning')}
                        </p>
                      )}
                    </div>
                  )
                })}
              </div>
            )}
          </div>
        ) : (
          <div className="flex flex-col gap-4">
            <div className="grid gap-4 sm:grid-cols-3">
              <Field
                label={t('policyEditor.requiredApprovals')}
                htmlFor="pol-required"
                description={t('policyEditor.requiredApprovalsHint')}
              >
                <Input
                  id="pol-required"
                  type="number"
                  min={0}
                  max={64}
                  value={requiredApprovals}
                  onChange={(e) => setRequiredApprovals(e.target.value)}
                  mono
                />
              </Field>
              <Field
                label={t('policyEditor.expiresInSeconds')}
                htmlFor="pol-expires"
                description={t('policyEditor.expiresInSecondsHint')}
              >
                <Input
                  id="pol-expires"
                  type="number"
                  min={0}
                  max={31536000}
                  value={expiresIn}
                  onChange={(e) => setExpiresIn(e.target.value)}
                  mono
                />
              </Field>
              <Field
                label={t('policyEditor.escalateInSeconds')}
                htmlFor="pol-escalate"
                description={t('policyEditor.escalateInSecondsHint')}
              >
                <Input
                  id="pol-escalate"
                  type="number"
                  min={0}
                  max={31536000}
                  value={escalateIn}
                  onChange={(e) => setEscalateIn(e.target.value)}
                  mono
                />
              </Field>
            </div>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field
                label={t('policyEditor.matchAction')}
                htmlFor="pol-match-action"
                description={t('policyEditor.matchActionHint')}
              >
                <Input
                  id="pol-match-action"
                  value={matchAction}
                  onChange={(e) => setMatchAction(e.target.value)}
                  mono
                />
              </Field>
              <Field
                label={t('policyEditor.matchSubjectKind')}
                htmlFor="pol-match-subject"
                description={t('policyEditor.matchSubjectKindHint')}
              >
                <Input
                  id="pol-match-subject"
                  value={matchSubjectKind}
                  onChange={(e) => setMatchSubjectKind(e.target.value)}
                  mono
                />
              </Field>
            </div>
          </div>
        )}
      </div>

      <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <ScrollText className="size-3.5 shrink-0" aria-hidden />
        {t('common:privileged.auditedNotice')}
      </p>

      <DialogFooter>
        <Button
          variant="secondary"
          onClick={onClose}
          disabled={mutation.isPending}
        >
          {t('common:actions.cancel')}
        </Button>
        <Button
          variant="primary"
          onClick={submit}
          disabled={!valid || mutation.isPending}
        >
          {mutation.isPending && <Spinner size="sm" aria-hidden />}
          {isEdit ? t('policyEditor.save') : t('policyEditor.create')}
        </Button>
      </DialogFooter>
    </>
  )
}
