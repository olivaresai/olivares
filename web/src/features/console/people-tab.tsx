// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'

import { currentLanguage } from '@/lib/i18n'
import {
  Copy,
  Plus,
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
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
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
import { toast } from '@/components/ui/toaster'
import { AAL, RequireAssurance } from '@/features/identity/assurance'
import { ListTruncationBadge } from '@/features/_intel'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import {
  consoleApi,
  consoleKeys,
  type InviteDTO,
  type OnboardedUser,
  type OnboardResult,
  type RosterMemberDTO,
} from './api'

const ROLES = ['viewer', 'editor', 'admin', 'owner'] as const

export function PeopleTab() {
  const { t } = useTranslation(['console', 'common'])
  const { activeTenant, can } = useAuth()
  const canOnboard = can('membership:write')
  const canReadMembers = can('user:read')
  const canManageMembers = can('user:write')
  // The internal-superadmin lifecycle is global and remains superadmin-only.
  const canReadSupers = can('user:read', { tenant: null })
  const canManageSupers = can('user:write', { tenant: null })
  const [onboardOpen, setOnboardOpen] = useState(false)
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

  const memberToggleMutation = usePrivilegedMutation<
    { member: RosterMemberDTO; active: boolean },
    unknown
  >({
    mutationFn: ({ member, active }) =>
      consoleApi.setMemberActive(member.user_id, active),
    invalidateKeys: () => [consoleKeys.members(activeTenant)],
    successMessage: (_data, vars) =>
      vars.active
        ? t('console:members.enabled')
        : t('console:members.disabled'),
    onDone: () => setMemberToggle(null),
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
  const supers = superadmins.data?.items ?? []
  const selectedMemberNextActive = memberToggle?.status !== 'active'

  return (
    <div className="flex flex-col gap-6 pt-4">
      <section className="flex flex-col gap-3">
        <div className="flex items-start justify-between gap-3">
          <div>
            <h2 className="text-base font-semibold text-foreground">
              {t('console:people.title')}
            </h2>
            <p className="max-w-2xl text-sm text-muted-foreground">
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
          <h3 className="text-sm font-semibold text-foreground">
            {t('console:members.title')}
          </h3>
          <p className="max-w-2xl text-sm text-muted-foreground">
            {t('console:members.caption')}
          </p>
          {roster.some((member) => member.sso_only || member.external_id) ? (
            <p className="mt-1 max-w-2xl text-xs text-muted-foreground">
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
          <ErrorState retry={() => void members.refetch()} />
        ) : roster.length === 0 ? (
          <EmptyState
            icon={<Users />}
            title={t('console:members.none')}
            description={t('console:members.noneHint')}
          />
        ) : (
          <div className="overflow-hidden rounded-lg border border-border">
            <table className="w-full text-sm">
              <thead className="bg-muted/40 text-left text-xs text-muted-foreground">
                <tr>
                  <th className="px-3 py-2 font-medium">
                    {t('console:members.user')}
                  </th>
                  <th className="px-3 py-2 font-medium">
                    {t('console:people.role')}
                  </th>
                  <th className="px-3 py-2 font-medium">
                    {t('console:members.groups')}
                  </th>
                  <th className="px-3 py-2 font-medium">
                    {t('console:members.status')}
                  </th>
                  <th className="px-3 py-2 text-right font-medium">
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
                    onToggle={setMemberToggle}
                  />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <section className="flex flex-col gap-3">
        <div>
          <h3 className="text-sm font-semibold text-foreground">
            {t('console:people.pendingTitle')}
          </h3>
          <p className="text-sm text-muted-foreground">
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
          <ErrorState retry={() => void invites.refetch()} />
        ) : items.length === 0 ? (
          <EmptyState title={t('console:people.noInvites')} />
        ) : (
          <div className="overflow-hidden rounded-lg border border-border">
            <table className="w-full text-sm">
              <thead className="bg-muted/40 text-left text-xs text-muted-foreground">
                <tr>
                  <th className="px-3 py-2 font-medium">
                    {t('console:people.email')}
                  </th>
                  <th className="px-3 py-2 font-medium">
                    {t('console:people.role')}
                  </th>
                  <th className="px-3 py-2 font-medium">
                    {t('console:people.expires')}
                  </th>
                  <th className="px-3 py-2" />
                </tr>
              </thead>
              <tbody>
                {items.map((inv) => (
                  <tr key={inv.id} className="border-t border-border">
                    <td className="px-3 py-2 font-medium text-foreground">
                      {inv.email}
                    </td>
                    <td className="px-3 py-2">
                      <Badge variant="neutral">{inv.role}</Badge>
                    </td>
                    <td className="px-3 py-2 text-muted-foreground">
                      {new Date(inv.expires_at).toLocaleDateString(
                        currentLanguage(),
                      )}
                    </td>
                    <td className="px-3 py-2 text-right">
                      {canOnboard && (
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => setRevoke(inv)}
                        >
                          <Trash2 />
                          {t('console:people.revoke')}
                        </Button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      {canReadSupers && (
        <section className="flex flex-col gap-3">
          <div>
            <h3 className="text-sm font-semibold text-foreground">
              {t('console:superadmins.title')}
            </h3>
            <p className="max-w-2xl text-sm text-muted-foreground">
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
            <ErrorState retry={() => void superadmins.refetch()} />
          ) : (
            <div className="overflow-hidden rounded-lg border border-border">
              <table className="w-full text-sm">
                <thead className="bg-muted/40 text-left text-xs text-muted-foreground">
                  <tr>
                    <th className="px-3 py-2 font-medium">
                      {t('console:people.email')}
                    </th>
                    <th className="px-3 py-2 font-medium">
                      {t('console:superadmins.statusHeader')}
                    </th>
                    <th className="px-3 py-2" />
                  </tr>
                </thead>
                <tbody>
                  {supers.map((u) => {
                    const active = u.status === 'active'
                    return (
                      <tr key={u.id} className="border-t border-border">
                        <td className="px-3 py-2 font-medium text-foreground">
                          {u.email}
                        </td>
                        <td className="px-3 py-2">
                          <Badge variant={active ? 'success' : 'neutral'}>
                            {active
                              ? t('console:superadmins.active')
                              : t('console:superadmins.inactive')}
                          </Badge>
                        </td>
                        <td className="px-3 py-2 text-right">
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
              </table>
            </div>
          )}
        </section>
      )}

      <Dialog open={onboardOpen} onOpenChange={setOnboardOpen}>
        <DialogContent className="max-h-[85vh] max-w-lg overflow-y-auto">
          {onboardOpen && (
            <RequireAssurance minAal={AAL.HARDWARE} action="console">
              <OnboardForm onClose={() => setOnboardOpen(false)} />
            </RequireAssurance>
          )}
        </DialogContent>
      </Dialog>

      {memberToggle ? (
        <RequireAssurance minAal={AAL.HARDWARE} action="console">
          <ConfirmDialog
            open
            onOpenChange={(open) => !open && setMemberToggle(null)}
            title={
              selectedMemberNextActive
                ? t('console:members.enableTitle')
                : t('console:members.disableTitle')
            }
            description={
              selectedMemberNextActive
                ? t('console:members.enableBody', { email: memberToggle.email })
                : t('console:members.disableBody', {
                    email: memberToggle.email,
                  })
            }
            confirmLabel={
              selectedMemberNextActive
                ? t('console:members.enable')
                : t('console:members.disable')
            }
            tone={selectedMemberNextActive ? 'default' : 'danger'}
            pending={memberToggleMutation.isPending}
            onConfirm={() =>
              memberToggleMutation.mutate({
                member: memberToggle,
                active: selectedMemberNextActive,
              })
            }
          >
            {memberToggle.sso_only || memberToggle.external_id ? (
              <p>{t('console:members.idpManagedConfirm')}</p>
            ) : null}
          </ConfirmDialog>
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
  onToggle,
}: {
  member: RosterMemberDTO
  canManage: boolean
  onToggle: (member: RosterMemberDTO) => void
}) {
  const { t } = useTranslation(['console', 'common'])
  const active = member.status === 'active'
  const displayName =
    member.display_name && member.display_name !== member.email
      ? member.display_name
      : undefined
  return (
    <tr className="border-t border-border align-top">
      <td className="px-3 py-2">
        <div className="flex flex-col gap-1">
          <span className="font-medium text-foreground">{member.email}</span>
          {displayName ? (
            <span className="text-xs text-muted-foreground">{displayName}</span>
          ) : null}
          {member.sso_only ? (
            <span>
              <Badge variant="info">{t('console:members.ssoOnly')}</Badge>
            </span>
          ) : null}
        </div>
      </td>
      <td className="px-3 py-2">
        <Badge variant="neutral">
          {t(`console:members.roles.${member.role}`, member.role)}
        </Badge>
      </td>
      <td className="px-3 py-2">
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
      <td className="px-3 py-2">
        <Badge variant={memberStatusVariant(member.status)}>
          {t(`console:members.statuses.${member.status}`, member.status)}
        </Badge>
      </td>
      <td className="px-3 py-2 text-right">
        {canManage ? (
          <Switch
            checked={active}
            onCheckedChange={() => onToggle(member)}
            aria-label={
              active
                ? t('console:members.toggleDisableLabel', {
                    email: member.email,
                  })
                : t('console:members.toggleEnableLabel', {
                    email: member.email,
                  })
            }
          />
        ) : (
          <span className="text-muted-foreground">-</span>
        )}
      </td>
    </tr>
  )
}

function memberStatusVariant(status: RosterMemberDTO['status']) {
  if (status === 'active') return 'success'
  if (status === 'error') return 'danger'
  return 'neutral'
}

function OnboardForm({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation(['console', 'common'])
  const { activeTenant } = useAuth()
  const [email, setEmail] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [role, setRole] = useState('editor')
  const [mode, setMode] = useState<'password' | 'invite'>('password')
  const [password, setPassword] = useState('')
  const [invite, setInvite] = useState<OnboardResult['invite'] | null>(null)

  const mutation = usePrivilegedMutation<void, OnboardResult>({
    mutationFn: () =>
      consoleApi.onboard({
        email: email.trim(),
        display_name: displayName.trim() || undefined,
        role,
        mode,
        password: mode === 'password' ? password : undefined,
      }),
    invalidateKeys: () => [consoleKeys.invites(activeTenant)],
    successMessage: t('console:onboard.created'),
    onDone: (data) => {
      // Invite mode shows the show-once link in place; password mode just closes.
      if (data.invite) setInvite(data.invite)
      else onClose()
    },
  })

  const valid =
    email.includes('@') && (mode === 'invite' || password.length >= 8)

  if (invite) {
    return (
      <>
        <DialogHeader>
          <DialogTitle>{t('console:onboard.inviteCreatedTitle')}</DialogTitle>
          <DialogDescription>
            {t('console:onboard.inviteCreatedBody', { email })}
          </DialogDescription>
        </DialogHeader>
        <div className="flex items-center gap-2">
          <Input readOnly value={invite.accept_url} mono />
          <Button
            variant="secondary"
            onClick={() => {
              void navigator.clipboard?.writeText(invite.accept_url)
              toast.success(t('console:onboard.copied'))
            }}
          >
            <Copy />
            {t('console:onboard.copyLink')}
          </Button>
        </div>
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
          required
        >
          <Input
            id="ob-email"
            type="email"
            value={email}
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
