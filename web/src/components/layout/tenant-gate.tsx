// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// TenantGate — no tenant selected ⇒ no tenant-scoped request.
//
// Every tenant-scoped route resolves its tenant from the caller's grants or the
// X-Olivares-Tenant header (core/api/middleware.go resolveTenantValue), and a
// SUPERADMIN never gets the single-membership default. So with nothing selected
// the console sends no header and EVERY tenant-scoped endpoint answers
// 400 bad_request "tenant required": /v1/workspaces, /v1/members, /v1/audit,
// /v1/agents, /v1/invites, /v1/search and every /v1/m/* module route. The estate
// overview alone mounts a dozen of those at once, so an install whose principal
// had no organization showed the operator a wall of failures and read as "the
// panel is broken".
//
// The engine's check is correct and stays. What was wrong is ASKING. This gate
// stands between the authenticated shell and the routed content: while there is
// no active tenant the routed view is NOT MOUNTED (its queries never exist), and
// the operator is shown the one action that unblocks them — create the first
// organization, or pick one. It never invents a tenant, and it never guesses when
// the organization list could not be read: that is an error with a retry, not an
// empty state (a gate must distinguish "none" from "I could not look").
//
// First-boot setup provisions the first organization and grants the superadmin
// ownership of it (core/api/handlers_auth.go handleSetup), and the setup page
// selects it — so on a fresh install this gate is invisible. It carries the two
// states that setup cannot: an account created before its organization existed,
// and a principal that belongs to none.
import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useRouterState } from '@tanstack/react-router'
import { Building2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState } from '@/components/ui/error-state'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/ui/spinner'
import { systemApi } from '@/lib/api/endpoints'
import { queryKeys } from '@/lib/api/query'
import type { OrgDTO } from '@/lib/api/types'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { slugify } from '@/lib/utils'

/**
 * The one route the gate lets through: /settings reads NOTHING tenant-scoped (local
 * preference stores plus the unauthenticated server-info), and the user menu points
 * straight at it. Gating it would strand a principal with no organization on a page
 * that works perfectly. The exception is named, not inferred — and the gate's test
 * asserts that page still issues no tenant-scoped request, so it cannot rot into a
 * hole if someone adds a tenant-scoped read there later.
 */
const TENANT_FREE_ROUTE = '/settings'

/** The shell's "still resolving" placeholder — same restraint as AppLayout's splash. */
function GateSpinner({ label }: { label: string }) {
  return (
    <div role="status" className="flex justify-center py-16">
      <span className="sr-only">{label}</span>
      <Spinner />
    </div>
  )
}

export function TenantGate({ children }: { children: ReactNode }) {
  const { t } = useTranslation(['auth', 'common'])
  const { activeTenant, grants, isSuperadmin, setActiveTenant } = useAuth()
  const pathname = useRouterState({ select: (s) => s.location.pathname })

  // Only a principal with NOTHING selected needs the organization list. A ready
  // console never pays for this read, and the key is the org switcher's, so the
  // two share one cache entry instead of issuing the same request twice.
  const undecided = !activeTenant
  const orgsQ = useQuery({
    queryKey: queryKeys.orgs,
    queryFn: () => systemApi.listOrgs(),
    enabled: undecided && isSuperadmin,
    staleTime: 60_000,
  })
  const orgs = useMemo(() => orgsQ.data?.items ?? [], [orgsQ.data])

  // One organization is not a choice. (AuthProvider already defaults a principal
  // that HAS grants; this covers the superadmin whose account predates any
  // organization — an install set up before setup provisioned one.)
  const only =
    undecided && isSuperadmin && orgs.length === 1 ? orgs[0] : undefined
  useEffect(() => {
    if (only) setActiveTenant(only.tenant_id)
  }, [only, setActiveTenant])

  // Resolution still runs on the pass-through route (a single organization is
  // selected while the operator sits in /settings); only the GATING is waived.
  if (!undecided || pathname === TENANT_FREE_ROUTE) return <>{children}</>

  // A principal that HAS a grant is one render away from a selection (the
  // AuthProvider tenant effect resolves it): show the placeholder, never a
  // first-run screen that would flash and vanish.
  if (grants.length > 0 || only)
    return <GateSpinner label={t('common:states.loading')} />

  // No grants and no system rights: the honest dead end. The topbar keeps the
  // user menu, so signing out is still one click away.
  if (!isSuperadmin)
    return (
      <EmptyState
        icon={<Building2 />}
        title={t('auth:tenant.none')}
        description={t('auth:tenant.noneHint')}
      />
    )

  if (orgsQ.isLoading)
    return <GateSpinner label={t('auth:firstRun.checking')} />

  // "I could not look" is not "there is none": never offer to create a first
  // organization on the strength of a failed read — it could create a second.
  if (orgsQ.isError)
    return (
      <ErrorState
        title={t('auth:firstRun.unavailableTitle')}
        description={t('auth:firstRun.unavailableDescription')}
        retry={() => void orgsQ.refetch()}
      />
    )

  if (orgs.length === 0) return <FirstOrgForm />
  return <OrgPicker orgs={orgs} onSelect={setActiveTenant} />
}

/** Create the first organization — the only write this gate offers. The engine
 * seeds its "Default" workspace and starts its audit chain in the same
 * transaction (core/internal/store/sqlstore/system.go CreateOrg), so selecting it
 * lands the operator on a console that works. */
function FirstOrgForm() {
  const { t } = useTranslation(['auth', 'common'])
  const { setActiveTenant } = useAuth()
  const queryClient = useQueryClient()
  const [name, setName] = useState('')
  const [slug, setSlug] = useState('')
  const [slugEdited, setSlugEdited] = useState(false)
  const effectiveSlug = slugEdited ? slug : slugify(name)

  const mut = usePrivilegedMutation<void, OrgDTO>({
    mutationFn: () =>
      systemApi.createOrg({ name: name.trim(), slug: effectiveSlug }),
    successMessage: t('auth:firstRun.created'),
    onDone: (org) => {
      // Selecting the new tenant is what makes the console usable: from here on
      // the client sends X-Olivares-Tenant and the routed view mounts against a
      // tenant that exists. We select the id the ENGINE returned in the CREATE
      // response, never one we assembled or re-derived from a later listing.
      //
      // It is deliberately NOT passed as `invalidateKeys`: that would run the
      // list refetch FIRST and only select afterwards, so a listing that is slow
      // or failing would strand the operator on this form for an organization
      // that already exists. Refresh the switcher's list in the background — a
      // list one refetch behind is cosmetic; a blocked selection is not.
      setActiveTenant(org.tenant_id)
      void queryClient.invalidateQueries({ queryKey: queryKeys.orgs })
    },
  })

  const valid = name.trim() !== '' && effectiveSlug !== ''

  return (
    <div className="flex justify-center py-10">
      <Card className="w-full max-w-xl p-6">
        <div className="mb-5 flex items-start gap-3">
          <div className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-muted text-muted-foreground">
            <Building2 className="size-5" aria-hidden />
          </div>
          <div className="flex flex-col gap-1">
            <h1 className="font-display text-lg font-semibold tracking-tight text-foreground">
              {t('auth:firstRun.createTitle')}
            </h1>
            <p className="text-sm text-muted-foreground">
              {t('auth:firstRun.createDescription')}
            </p>
          </div>
        </div>
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault()
            if (valid && !mut.isPending) mut.mutate()
          }}
        >
          <Field label={t('auth:firstRun.name')}>
            {({ id }) => (
              <Input
                id={id}
                value={name}
                autoFocus
                onChange={(e) => setName(e.target.value)}
                placeholder={t('auth:firstRun.namePlaceholder')}
                autoComplete="off"
              />
            )}
          </Field>
          <Field label={t('auth:firstRun.slug')}>
            {({ id }) => (
              <Input
                id={id}
                value={effectiveSlug}
                onChange={(e) => {
                  setSlug(e.target.value)
                  setSlugEdited(true)
                }}
                mono
                autoComplete="off"
              />
            )}
          </Field>
          <div>
            <Button
              type="submit"
              variant="primary"
              disabled={!valid || mut.isPending}
            >
              {mut.isPending && <Spinner size="sm" aria-hidden />}
              {t('auth:firstRun.create')}
            </Button>
          </div>
        </form>
      </Card>
    </div>
  )
}

/** Pick the organization to work in. Reached only by a superadmin holding no
 * membership in any of SEVERAL organizations — with exactly one, TenantGate
 * selects it rather than asking a question with one answer. */
function OrgPicker({
  orgs,
  onSelect,
}: {
  orgs: OrgDTO[]
  onSelect: (tenant: string) => void
}) {
  const { t } = useTranslation(['auth', 'common'])
  return (
    <div className="flex justify-center py-10">
      <Card className="w-full max-w-xl p-6">
        <div className="mb-5 flex flex-col gap-1">
          <h1 className="font-display text-lg font-semibold tracking-tight text-foreground">
            {t('auth:firstRun.chooseTitle')}
          </h1>
          <p className="text-sm text-muted-foreground">
            {t('auth:firstRun.chooseDescription')}
          </p>
        </div>
        <ul className="flex flex-col gap-2">
          {orgs.map((o) => (
            <li key={o.tenant_id}>
              <Button
                variant="secondary"
                className="w-full justify-start gap-2"
                onClick={() => onSelect(o.tenant_id)}
              >
                <Building2
                  className="size-4 text-muted-foreground"
                  aria-hidden
                />
                <span className="truncate">{o.name}</span>
                <span className="truncate font-mono text-xs text-muted-foreground">
                  {o.slug}
                </span>
              </Button>
            </li>
          ))}
        </ul>
      </Card>
    </div>
  )
}
