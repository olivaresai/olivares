// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { ScrollArea } from '@/components/ui/scroll-area'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { Label } from '@/components/ui/label'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { governanceApi, governanceKeys } from './api'
import './i18n'

export interface GroupMembersSheetProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** The group's directory ref string (NOT an internal id). */
  groupRef: string | null
}

/**
 * GroupMembersSheet drills into a reconciled collection's members. The Direct /
 * Transitive toggle (?transitive=true) walks nested collections down to leaf
 * identities (bounded depth, cycle-safe) and surfaces the nesting path (`via`). This
 * endpoint drains internally — no cursor / "load more". {ref} is the directory ref.
 */
export function GroupMembersSheet({
  open,
  onOpenChange,
  groupRef,
}: GroupMembersSheetProps) {
  const { t } = useTranslation('governance')
  const { activeTenant } = useAuth()
  const [transitive, setTransitive] = useState(false)

  const query = useQuery({
    queryKey: governanceKeys.groupMembers(activeTenant, groupRef ?? '', {
      transitive,
    }),
    queryFn: () =>
      governanceApi.listGroupMembers(
        groupRef!,
        transitive ? { transitive } : undefined,
      ),
    enabled: open && !!groupRef,
  })

  const members = query.data?.items ?? []

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full sm:max-w-lg">
        <SheetHeader>
          <SheetTitle>{t('members.title')}</SheetTitle>
          <SheetDescription>
            {t('members.subtitle', { group: groupRef ?? '' })}
          </SheetDescription>
        </SheetHeader>

        <div className="flex items-center justify-between gap-2 rounded-md border border-border bg-muted/40 px-3 py-2">
          <div className="flex items-center gap-2">
            <Switch
              id="members-transitive"
              checked={transitive}
              onCheckedChange={setTransitive}
            />
            <Label htmlFor="members-transitive">
              {t('members.transitive')}
            </Label>
          </div>
          <p className="text-xs text-muted-foreground">
            {t('members.transitiveHint')}
          </p>
        </div>

        <ScrollArea className="-mr-4 flex-1 pr-4">
          {query.isLoading ? (
            <div className="flex flex-col gap-2">
              {Array.from({ length: 5 }).map((_, i) => (
                <Skeleton key={i} className="h-12 w-full" />
              ))}
            </div>
          ) : query.error instanceof ApiError &&
            query.error.isStepUpRequired ? (
            /* Aseguramiento antes que rol: `isForbidden` es sólo el status
               (lib/api/errors.ts:59) y el 403 de ceremonia lo satisface también. */
            <StepUpRequiredState
              action="generic"
              onElevated={() => void query.refetch()}
            />
          ) : query.error instanceof ApiError && query.error.isForbidden ? (
            <ForbiddenState />
          ) : query.error ? (
            <ErrorState retry={() => query.refetch()} />
          ) : members.length === 0 ? (
            <EmptyState title={t('members.empty')} />
          ) : (
            <ul className="flex flex-col gap-2">
              {members.map((m, i) => (
                <li
                  key={`${m.member_ref}:${i}`}
                  className="flex items-center justify-between gap-2 rounded-md border border-border bg-surface px-2.5 py-1.5"
                >
                  <span className="truncate font-mono text-xs text-foreground">
                    {m.member_ref}
                  </span>
                  <div className="flex shrink-0 items-center gap-1.5">
                    {m.via && (
                      <span className="text-xs text-muted-foreground">
                        {t('members.via')}{' '}
                        <span className="font-mono">{m.via}</span>
                      </span>
                    )}
                    <Badge
                      variant={
                        m.member_kind === 'collection' ? 'info' : 'neutral'
                      }
                    >
                      {m.member_kind === 'collection'
                        ? t('members.kindCollection')
                        : t('members.kindIdentity')}
                    </Badge>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </ScrollArea>
      </SheetContent>
    </Sheet>
  )
}
