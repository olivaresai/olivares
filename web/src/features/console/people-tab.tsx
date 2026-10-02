// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryErrorState } from '@/components/layout/query-error-state'
import { useQuery } from '@tanstack/react-query'

import { currentLanguage } from '@/lib/i18n'
import {
  Plus,
  Send,
  ShieldCheck,
  ShieldOff,
  Trash2,
  UserPlus,
  Users,
} from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { ForbiddenState } from '@/components/ui/error-state'
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
import { AAL, RequireAssurance } from '@/features/identity/assurance'
import { ListTruncationBadge } from '@/features/_intel'
import { ApiError } from '@/lib/api/errors'
import { isEngineEmail } from '@/lib/email'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import {
  consoleApi,
  consoleKeys,
  isConsentRequired,
  type InviteDTO,
  type InviteHandle,
  type OnboardedUser,
  type OnboardResponse,
  type OnboardResult,
  type RosterMemberDTO,
} from './api'
import { XScroll } from '@/components/data/scroll-edges'
import { StaticTable } from '@/components/data/static-table'

const ROLES = ['viewer', 'editor', 'admin', 'owner'] as const

export function PeopleTab({
  inviteRequested = false,
  onInviteHandled,
}: {
  /** ⌘K "Invite people" (identity-view.tsx): open the onboarding dialog in its invite
   * mode, behind the same step-up as the button, for a principal who may onboard. */
  inviteRequested?: boolean
  onInviteHandled?: () => void
} = {}) {
  const { t } = useTranslation(['console', 'common'])
  const { activeTenant, can, principal } = useAuth()
  const canOnboard = can('membership:write')
  const canReadMembers = can('user:read')
  const canManageMembers = can('user:write')
  // The internal-superadmin lifecycle is global and remains superadmin-only.
  const canReadSupers = can('user:read', { tenant: null })
  const canManageSupers = can('user:write', { tenant: null })
  const [onboardButtonOpen, setOnboardButtonOpen] = useState(false)
  const onboardOpen = onboardButtonOpen || (inviteRequested && canOnboard)
  const setOnboardOpen = (open: boolean) => {
    setOnboardButtonOpen(open)
    if (!open) onInviteHandled?.()
  }
  const [revoke, setRevoke] = useState<InviteDTO | null>(null)
  const [toggle, setToggle] = useState<OnboardedUser | null>(null)
  const [memberToggle, setMemberToggle] = useState<RosterMemberDTO | null>(null)

  const invites = useQuery({
    queryKey: consoleKeys.invites(activeTenant),
    queryFn: () => consoleApi.listInvites(),
    enabled: can('membership:read'),
  })

  const members = useQuery({
    queryKey: consoleKeys.members(activeTenant),
    queryFn: () => consoleApi.listMembers(),
    enabled: canReadMembers,
  })

  const superadmins = useQuery({
    queryKey: consoleKeys.superadmins(),
    queryFn: () => consoleApi.listSuperadmins(),
    enabled: canReadSupers,
  })

  const revokeMutation = usePrivilegedMutation<string, void>({
    mutationFn: (id) => consoleApi.revokeInvite(id),
    invalidateKeys: () => [consoleKeys.invites(activeTenant)],
    successMessage: t('console:people.revoked'),
    onDone: () => setRevoke(null),
  })

  // A resend mails the invitee a new link; the console never sees it.
  const resendMutation = usePrivilegedMutation<string, InviteHandle>({
    mutationFn: (id) => consoleApi.resendInvite(id),
    invalidateKeys: () => [consoleKeys.invites(activeTenant)],
    successMessage: (data) =>
      data.delivery === 'sent'
        ? t('console:people.resent')
        : t('console:people.resendFailed'),
  })

  // A tenant can only remove a member. Nothing here re-activates an account:
  // its global status belongs to the deployment.
  const memberToggleMutation = usePrivilegedMutation<RosterMemberDTO, unknown>({
    mutationFn: (member) => consoleApi.setMemberActive(member.user_id, false),
    invalidateKeys: () => [consoleKeys.members(activeTenant)],
    successMessage: t('console:members.disabled'),
    onDone: () => setMemberToggle(null),
  })

  // the lost-device reset of a member's TOTP factor. The confirm dialog is
  // the guard the row's one-click label alone cannot be.
  const [totpResetTarget, setTotpResetTarget] =
    useState<RosterMemberDTO | null>(null)
  const memberTOTPResetMutation = usePrivilegedMutation<
    RosterMemberDTO,
    unknown
  >({
    mutationFn: (member) => consoleApi.resetMemberTOTP(member.user_id),
    invalidateKeys: () => [consoleKeys.members(activeTenant)],
    successMessage: t('console:members.totpResetDone'),
    onDone: () => setTotpResetTarget(null),
  })

  // Flip a superadmin: an active account is disabled, an inactive one re-enabled.
  const toggleMutation = usePrivilegedMutation<OnboardedUser, OnboardedUser>({
    mutationFn: (u) =>
      consoleApi.setSuperadminActive(u.id, u.status !== 'active'),
    invalidateKeys: () => [consoleKeys.superadmins()],
    successMessage: (data) =>
      data.status === 'active'
        ? t('console:superadmins.enabled')
        : t('console:superadmins.disabled'),
    onDone: () => setToggle(null),
  })

  const items = invites.data?.items ?? []
  const roster = members.data?.items ?? []
  const activeOwners = roster.filter(
    (m) => m.role === 'owner' && m.status === 'active',
  ).length
  const supers = superadmins.data?.items ?? []

  return (
    <div className="flex flex-col gap-6 pt-4">
      <section className="flex flex-col gap-3">
        <div className="flex items-start justify-between gap-3">
          <div>
            <h2 className="text-heading text-foreground">
              {t('console:people.title')}
            </h2>
            <p className="max-w-2xl text-body text-muted-foreground">
              {t('console:people.caption')}
            </p>
          </div>
          {canOnboard && (
            <Button onClick={() => setOnboardOpen(true)}>
              <UserPlus />
              {t('console:people.onboard')}
            </Button>
          )}
        </div>
      </section>

      <section className="flex flex-col gap-3">
        <div>
          <h3 className="text-body font-semibold text-foreground">
            {t('console:members.title')}
          </h3>
          <p className="max-w-2xl text-body text-muted-foreground">
            {t('console:members.caption')}
          </p>
          {roster.some((member) => member.sso_only || member.external_id) ? (
            <p className="mt-1 max-w-2xl text-caption text-muted-foreground">
              {t('console:members.externalManagedNotice')}
            </p>
          ) : null}
        </div>
        <ListTruncationBadge
          query={members}
          label={t('intel:notices.listTruncated', {
            n: members.data?.items?.length ?? 0,
          })}
          hint={t('intel:notices.listTruncatedHint')}
          className="px-0 pt-0 pb-3"
          filas={members.data?.items?.length ?? 0}
        />
        {!canReadMembers ? (
          <ForbiddenState
            icon={<ShieldOff />}
            title={t('console:members.readOnlyNotice')}
          />
        ) : members.isLoading ? (
          <div className="flex justify-center py-8">
            <Spinner />
          </div>
        ) : members.isError ? (
          <QueryErrorState
            error={members.error}
            retry={() => void members.refetch()}
          />
        ) : roster.length === 0 ? (
          <EmptyState
            icon={<Users />}
            title={t('console:members.none')}
            description={t('console:members.noneHint')}
          />
        ) : (
          <div className="overflow-hidden rounded-lg border border-border">
            <XScroll>
              <StaticTable>
                <thead>
                  <tr>
                    <th>{t('console:members.user')}</th>
                    <th>{t('console:people.role')}</th>
                    <th>{t('console:members.groups')}</th>
                    <th>{t('console:members.status')}</th>
                    <th className="text-right">
                      {t('console:members.action')}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {roster.map((member) => (
                    <RosterMemberRow
                      key={member.user_id}
                      member={member}
                      canManage={canManageMembers}
                      // Never offered on your own row or on the organization's only
                      // active owner: either would lock the organization out (HU-25).
                      canRemove={
                        member.user_id !== principal?.user_id &&
                        !(member.role === 'owner' && activeOwners <= 1)
                      }
                      onToggle={setMemberToggle}
                      onResetTOTP={setTotpResetTarget}
                    />
                  ))}
                </tbody>
              </StaticTable>
            </XScroll>
          </div>
        )}
      </section>

      <section className="flex flex-col gap-3">
        <div>
          <h3 className="text-body font-semibold text-foreground">
            {t('console:people.pendingTitle')}
          </h3>
          <p className="text-body text-muted-foreground">
            {t('console:people.pendingCaption')}
          </p>
        </div>
        <ListTruncationBadge
          query={invites}
          label={t('intel:notices.listTruncated', {
            n: invites.data?.items?.length ?? 0,
          })}
          hint={t('intel:notices.listTruncatedHint')}
          className="px-0 pt-0 pb-3"
          filas={invites.data?.items?.length ?? 0}
        />
        {invites.isLoading ? (
          <div className="flex justify-center py-8">
            <Spinner />
          </div>
        ) : invites.isError ? (
          <QueryErrorState
            error={invites.error}
            retry={() => void invites.refetch()}
          />
        ) : items.length === 0 ? (
          /* ⛔ THE SENTENCE SAID "Invite someone to add the first one" AND OFFERED NO
             WAY TO. The control exists, at the top of a page with the whole user
             table between it and this panel, so from here it is an instruction
             about somewhere else. The action opens the same dialog, under the same
             right, and its name says which invitation it makes — two controls with
             one accessible name is how the last attempt at this broke 17 tests. */
          <EmptyState
            description={t('console:people.noInvitesHint')}
            title={t('console:people.noInvites')}
            action={
              canOnboard ? (
                <Button size="sm" onClick={() => setOnboardOpen(true)}>
                  {t('console:people.inviteFirst')}
                </Button>
              ) : null
            }
          />
        ) : (
          <div className="overflow-hidden rounded-lg border border-border">
            <XScroll>
              <StaticTable>
                <thead>
                  <tr>
                    <th>{t('console:people.email')}</th>
                    <th>{t('console:people.role')}</th>
                    <th>{t('console:people.expires')}</th>
                    <th />
                  </tr>
                </thead>
                <tbody>
                  {items.map((inv) => (
                    <tr key={inv.id}>
                      <td className="font-medium text-foreground">
                        {inv.email}
                      </td>
                      <td>
                        <Badge variant="neutral">{inv.role}</Badge>
                      </td>
                      <td className="text-muted-foreground">
                        {new Date(inv.expires_at).toLocaleDateString(
                          currentLanguage(),
                        )}
                      </td>
                      <td className="text-right">
                        {canOnboard && (
                          <>
                            <Button
                              variant="ghost"
                              size="sm"
                              disabled={resendMutation.isPending}
                              onClick={() => resendMutation.mutate(inv.id)}
                            >
                              <Send />
                              {t('console:people.resend')}
                            </Button>
                            <Button
                              variant="ghost"
                              size="sm"
                              onClick={() => setRevoke(inv)}
                            >
                              <Trash2 />
                              {t('console:people.revoke')}
                            </Button>
                          </>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </StaticTable>
            </XScroll>
          </div>
        )}
      </section>

      {canReadSupers && (
        <section className="flex flex-col gap-3">
          <div>
            <h3 className="text-body font-semibold text-foreground">
              {t('console:superadmins.title')}
            </h3>
            <p className="max-w-2xl text-body text-muted-foreground">
              {t('console:superadmins.caption')}
            </p>
          </div>
          <ListTruncationBadge
            query={superadmins}
            label={t('intel:notices.listTruncated', {
              n: superadmins.data?.items?.length ?? 0,
            })}
            hint={t('intel:notices.listTruncatedHint')}
            className="px-0 pt-0 pb-3"
            filas={superadmins.data?.items?.length ?? 0}
          />
          {superadmins.isLoading ? (
            <div className="flex justify-center py-8">
              <Spinner />
            </div>
          ) : superadmins.isError ? (
            <QueryErrorState
              error={superadmins.error}
              retry={() => void superadmins.refetch()}
            />
          ) : (
            <div className="overflow-hidden rounded-lg border border-border">
              <XScroll>
                <StaticTable>
                  <thead>
                    <tr>
                      <th>{t('console:people.email')}</th>
                      <th>{t('console:superadmins.statusHeader')}</th>
                      <th />
                    </tr>
                  </thead>
                  <tbody>
                    {supers.map((u) => {
                      const active = u.status === 'active'
                      return (
                        <tr key={u.id}>
                          <td className="font-medium text-foreground">
                            {u.email}
                          </td>
                          <td>
                            <Badge variant={active ? 'success' : 'neutral'}>
                              {active
                                ? t('console:superadmins.active')
                                : t('console:superadmins.inactive')}
                            </Badge>
                          </td>
                          <td className="text-right">
                            {canManageSupers && (
                              <Button
                                variant="ghost"
                                size="sm"
                                onClick={() => setToggle(u)}
                              >
                                {active ? <ShieldOff /> : <ShieldCheck />}
                                {active
                                  ? t('console:superadmins.disable')
                                  : t('console:superadmins.enable')}
                              </Button>
                            )}
                          </td>
                        </tr>
                      )
                    })}
                  </tbody>
                </StaticTable>
              </XScroll>
            </div>
          )}
        </section>
      )}

      <Dialog open={onboardOpen} onOpenChange={setOnboardOpen}>
        <DialogContent className="max-h-[85vh] max-w-lg overflow-y-auto">
          {onboardOpen && (
            <RequireAssurance minAal={AAL.HARDWARE} action="console">
              <OnboardForm
                initialMode={
                  inviteRequested && !onboardButtonOpen ? 'invite' : 'password'
                }
                onClose={() => setOnboardOpen(false)}
              />
            </RequireAssurance>
          )}
        </DialogContent>
      </Dialog>

      {memberToggle ? (
        <RequireAssurance minAal={AAL.HARDWARE} action="console">
          <ConfirmDialog
            open
            onOpenChange={(open) => !open && setMemberToggle(null)}
            title={t('console:members.disableTitle')}
            description={t('console:members.disableBody', {
              email: memberToggle.email,
            })}
            confirmLabel={t('console:members.disable')}
            tone="danger"
            pending={memberToggleMutation.isPending}
            onConfirm={() => memberToggleMutation.mutate(memberToggle)}
          >
            {memberToggle.sso_only || memberToggle.external_id ? (
              <p>{t('console:members.idpManagedConfirm')}</p>
            ) : null}
          </ConfirmDialog>
        </RequireAssurance>
      ) : null}

      {totpResetTarget ? (
        <RequireAssurance minAal={AAL.HARDWARE} action="console">
          <ConfirmDialog
            open
            onOpenChange={(open) => !open && setTotpResetTarget(null)}
            title={t('console:members.totpResetTitle')}
            description={t('console:members.totpResetBody', {
              email: totpResetTarget.email,
            })}
            confirmLabel={t('console:members.totpReset')}
            tone="danger"
            pending={memberTOTPResetMutation.isPending}
            onConfirm={() => memberTOTPResetMutation.mutate(totpResetTarget)}
          />
        </RequireAssurance>
      ) : null}

      <Dialog
        open={toggle !== null}
        onOpenChange={(o) => !o && setToggle(null)}
      >
        <DialogContent className="max-w-md">
          {toggle && (
            <RequireAssurance minAal={AAL.HARDWARE} action="console">
              <ToggleSuperadminPanel
                user={toggle}
                pending={toggleMutation.isPending}
                onCancel={() => setToggle(null)}
                onConfirm={() => toggleMutation.mutate(toggle)}
              />
            </RequireAssurance>
          )}
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={revoke !== null}
        onOpenChange={(o) => !o && setRevoke(null)}
        title={t('console:people.revokeTitle')}
        description={t('console:people.revokeBody', {
          email: revoke?.email ?? '',
        })}
        confirmLabel={t('console:people.revoke')}
        tone="danger"
        pending={revokeMutation.isPending}
        onConfirm={() => revoke && revokeMutation.mutate(revoke.id)}
      />
    </div>
  )
}

function RosterMemberRow({
  member,
  canManage,
  canRemove,
  onToggle,
  onResetTOTP,
}: {
  member: RosterMemberDTO
  canManage: boolean
  /** False on your own row and on the only active owner. */
  canRemove: boolean
  onToggle: (member: RosterMemberDTO) => void
  onResetTOTP: (member: RosterMemberDTO) => void
}) {
  const { t } = useTranslation(['console', 'common'])
  const active = member.status === 'active'
  const displayName =
    member.display_name && member.display_name !== member.email
      ? member.display_name
      : undefined
  return (
    <tr>
      <td>
        {/* ONE LINE, name › detail › meta. The member cell stacked the address over
            the display name over an SSO badge, three rows of one line each, and the
            row measured 61 px against the 36 px table budget while every other
            column held one word. The address is the element that never gives way;
            the display name truncates before it and keeps its full value on
            `title`; the badge is meta and comes last. */}
        <div className="flex min-w-0 items-center gap-2">
          <span className="min-w-0 flex-1 truncate font-medium text-foreground">
            {member.email}
          </span>
          {displayName ? (
            <span
              className="min-w-0 shrink truncate text-caption text-muted-foreground"
              title={displayName}
            >
              {displayName}
            </span>
          ) : null}
          {member.sso_only ? (
            <Badge variant="info" className="shrink-0">
              {t('console:members.ssoOnly')}
            </Badge>
          ) : null}
        </div>
      </td>
      <td>
        <Badge variant="neutral">
          {t(`console:members.roles.${member.role}`, member.role)}
        </Badge>
      </td>
      <td>
        {member.groups?.length ? (
          <div className="flex flex-wrap gap-1">
            {member.groups.map((group) => (
              <Badge key={group} variant="outline">
                {group}
              </Badge>
            ))}
          </div>
        ) : (
          <span className="text-muted-foreground">
            {t('console:members.noGroups')}
          </span>
        )}
      </td>
      <td>
        <Badge variant={memberStatusVariant(member.status)}>
          {t(`console:members.statuses.${member.status}`, member.status)}
        </Badge>
      </td>
      <td className="text-right">
        <div className="flex items-center justify-end gap-2">
          {canManage && !member.sso_only ? (
            <Button
              variant="ghost"
              size="sm"
              onClick={() => onResetTOTP(member)}
              aria-label={t('console:members.totpResetLabel', {
                email: member.email,
              })}
            >
              {t('console:members.totpReset')}
            </Button>
          ) : null}
          {/* Removing a member is an action with a confirmation, never a switch: a
              switch read "on" beside "Remove" (HU-25). */}
          {canManage && active && canRemove ? (
            <Button
              variant="ghost"
              size="sm"
              onClick={() => onToggle(member)}
              aria-label={t('console:members.toggleDisableLabel', {
                email: member.email,
              })}
            >
              {t('console:members.disable')}
            </Button>
          ) : canManage && active ? null : canManage && canRemove ? (
            // An account the deployment suspended cannot be re-activated by an
            // organization; removing it is the one thing this organization can do.
            <div className="flex items-center justify-end gap-2">
              <span className="text-caption text-muted-foreground">
                {t('console:members.suspendedHint')}
              </span>
              <Button
                variant="ghost"
                size="sm"
                onClick={() => onToggle(member)}
                aria-label={t('console:members.toggleDisableLabel', {
                  email: member.email,
                })}
              >
                {t('console:members.disable')}
              </Button>
            </div>
          ) : (
            <span className="text-muted-foreground">-</span>
          )}
        </div>
      </td>
    </tr>
  )
}

function memberStatusVariant(status: RosterMemberDTO['status']) {
  if (status === 'active') return 'success'
  if (status === 'error') return 'danger'
  return 'neutral'
}

function OnboardForm({
  onClose,
  initialMode = 'password',
}: {
  onClose: () => void
  initialMode?: 'password' | 'invite'
}) {
  const { t } = useTranslation(['console', 'common'])
  const { activeTenant } = useAuth()
  const [email, setEmail] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [role, setRole] = useState('editor')
  const [mode, setMode] = useState<'password' | 'invite'>(initialMode)
  const [password, setPassword] = useState('')
  const [invite, setInvite] = useState<OnboardResult['invite'] | null>(null)
  const [consent, setConsent] = useState(false)
  // The engine's own sentence when it refuses the request (ID's account sweep: a malformed
  // address came back "User onboarded."). Shown in the form, not as a generic toast.
  const [refused, setRefused] = useState('')

  const mutation = usePrivilegedMutation<void, OnboardResponse>({
    mutationFn: () => {
      setRefused('')
      return consoleApi.onboard({
        email: email.trim(),
        display_name: displayName.trim() || undefined,
        role,
        mode,
        password: mode === 'password' ? password : undefined,
      })
    },
    onError: (err) => {
      if (!(err instanceof ApiError) || err.status !== 400 || !err.message)
        return false
      setRefused(err.message)
      return true
    },
    // The roster too: a new account is a member at once, and the Users table showed only
    // the administrator until the page was left (ID's account sweep, P2).
    invalidateKeys: () => [
      consoleKeys.invites(activeTenant),
      consoleKeys.members(activeTenant),
    ],
    successMessage: (data) =>
      isConsentRequired(data)
        ? t('console:onboard.consentRequiredToast')
        : t('console:onboard.created'),
    onDone: (data) => {
      // An existing account is never added here: it joins only by its holder's
      // consent. An invitation is mailed to the invitee; its link never comes back.
      if (isConsentRequired(data)) setConsent(true)
      else if (data.invite) setInvite(data.invite)
      else onClose()
    },
  })

  // The engine's own rule, the one sign-in uses (lib/email): an address the engine would
  // refuse is not sent ("bad@" passed the old check, which only looked for an @), and an
  // internal-domain address the engine accepts is not refused.
  const emailValid = isEngineEmail(email)
  const valid = emailValid && (mode === 'invite' || password.length >= 8)

  if (consent) {
    return (
      <>
        <DialogHeader>
          <DialogTitle>{t('console:onboard.consentRequiredTitle')}</DialogTitle>
          <DialogDescription>
            {t('console:onboard.consentRequiredBody', { email })}
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button onClick={onClose}>{t('console:onboard.done')}</Button>
        </DialogFooter>
      </>
    )
  }

  if (invite) {
    return (
      <>
        <DialogHeader>
          <DialogTitle>{t('console:onboard.inviteCreatedTitle')}</DialogTitle>
          <DialogDescription>
            {invite.delivery === 'sent'
              ? t('console:onboard.inviteSentBody', { email })
              : t('console:onboard.inviteFailedBody', { email })}
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button onClick={onClose}>{t('console:onboard.done')}</Button>
        </DialogFooter>
      </>
    )
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>{t('console:onboard.title')}</DialogTitle>
        <DialogDescription>{t('console:onboard.body')}</DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        <Field
          label={t('console:people.email')}
          htmlFor="ob-email"
          description={t('console:onboard.emailHint')}
          error={
            email.trim() && !emailValid
              ? t('common:validation.email')
              : undefined
          }
          required
        >
          <Input
            id="ob-email"
            type="email"
            value={email}
            aria-invalid={!!email.trim() && !emailValid}
            onChange={(e) => setEmail(e.target.value)}
          />
        </Field>
        <Field
          label={t('console:onboard.displayName')}
          htmlFor="ob-name"
          description={t('console:onboard.displayNameHint')}
        >
          <Input
            id="ob-name"
            value={displayName}
            onChange={(e) => setDisplayName(e.target.value)}
          />
        </Field>
        <Field
          label={t('console:people.role')}
          htmlFor="ob-role"
          description={t('console:onboard.roleHint')}
        >
          <Select value={role} onValueChange={setRole}>
            <SelectTrigger id="ob-role">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {ROLES.map((r) => (
                <SelectItem key={r} value={r}>
                  {r}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        <Field label={t('console:onboard.mode')} htmlFor="ob-mode">
          <Select
            value={mode}
            onValueChange={(v) => setMode(v as 'password' | 'invite')}
          >
            <SelectTrigger id="ob-mode">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="password">
                {t('console:onboard.modePassword')}
              </SelectItem>
              <SelectItem value="invite">
                {t('console:onboard.modeInvite')}
              </SelectItem>
            </SelectContent>
          </Select>
        </Field>
        {mode === 'password' && (
          <Field
            label={t('console:onboard.password')}
            htmlFor="ob-pass"
            description={t('console:onboard.passwordHint')}
            required
          >
            <Input
              id="ob-pass"
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </Field>
        )}
        {refused ? (
          <p role="alert" className="text-body text-danger">
            {refused}
          </p>
        ) : null}
      </div>

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
          onClick={() => mutation.mutate()}
          disabled={!valid || mutation.isPending}
        >
          {mutation.isPending ? <Spinner size="sm" aria-hidden /> : <Plus />}
          {t('console:onboard.submit')}
        </Button>
      </DialogFooter>
    </>
  )
}

// ToggleSuperadminPanel confirms enabling/disabling an internal superadmin, behind
// the AAL3 step-up its caller wraps it in. Disabling is reversible (the backend marks
// the account inactive and revokes its credentials, never deletes it) and deny-closed
// against disabling the last active superadmin — surfaced as a toast if attempted.
function ToggleSuperadminPanel({
  user,
  pending,
  onCancel,
  onConfirm,
}: {
  user: OnboardedUser
  pending: boolean
  onCancel: () => void
  onConfirm: () => void
}) {
  const { t } = useTranslation(['console', 'common'])
  const active = user.status === 'active'
  return (
    <>
      <DialogHeader>
        <DialogTitle>
          {active
            ? t('console:superadmins.disableTitle')
            : t('console:superadmins.enableTitle')}
        </DialogTitle>
        <DialogDescription>
          {active
            ? t('console:superadmins.disableBody', { email: user.email })
            : t('console:superadmins.enableBody', { email: user.email })}
        </DialogDescription>
      </DialogHeader>
      <DialogFooter>
        <Button variant="secondary" onClick={onCancel} disabled={pending}>
          {t('common:actions.cancel')}
        </Button>
        <Button
          variant={active ? 'destructive' : 'primary'}
          onClick={onConfirm}
          disabled={pending}
        >
          {pending ? (
            <Spinner size="sm" aria-hidden />
          ) : active ? (
            <ShieldOff />
          ) : (
            <ShieldCheck />
          )}
          {active
            ? t('console:superadmins.disable')
            : t('console:superadmins.enable')}
        </Button>
      </DialogFooter>
    </>
  )
}
