// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// AREA DIRECTORY (N1) — the page behind each `/areas/<id>` route.
//
// A directory is a page of LINKS: the area's task question, its sections, and for every entry
// this principal may open, the console's own label (the same nav:items key the sidebar and the
// breadcrumb use) and one sentence about the task. The link is the title; nothing else on a
// card is a control, so there is never a control nested inside a link.
//
// It mounts no module and issues no request. In particular it shows no counts, no last
// activity, no availability, no licence and no "ready" tick: none of those has an aggregate
// contract every principal may read, and a zero or a green tick standing in for a reading
// nobody made is the defect the proposal names (§3, "Portada"). "Open" is a navigation; what
// the operator may DO on the screen is decided there, by its own gates and by the engine.
//
// Visibility is the union of the area's authorized leaves. A direct link to an area with
// nothing this principal may open renders the localized empty state and a way back to the
// overview — it lists neither the denied entries nor anything about them. The shell shows its
// splash while the principal loads, so a still-loading auth never reads as an empty area.
import { Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { PageHeader } from '@/components/ui/page-header'
import { useViewAccess } from './authorization'
import type { AreaId, FeatureView } from '@/features/registry'
import {
  SETTINGS_UTILITY,
  areaById,
  areaLabel,
  areaQuestion,
  authorizedSections,
  sectionLabel,
  viewDescription,
  viewLabel,
} from './model'

interface DirectoryEntry {
  id: string
  path: string
  icon: FeatureView['icon']
  label: string
  description: string
}

function EntryRow({ entry }: { entry: DirectoryEntry }) {
  const Icon = entry.icon
  return (
    <li className="min-w-0">
      <div className="flex min-h-10 items-center gap-3 border-b border-border px-1 hover:bg-surface focus-within:bg-surface">
        <Icon
          aria-hidden="true"
          className="size-4 shrink-0 text-muted-foreground"
        />
        <h3 className="shrink-0 text-body font-medium">
          <Link
            to={entry.path as never}
            className="rounded-sm text-foreground outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background"
          >
            {entry.label}
          </Link>
        </h3>
        {entry.description ? (
          <p
            className="min-w-0 truncate text-caption text-muted-foreground"
            title={entry.description}
          >
            {entry.description}
          </p>
        ) : null}
      </div>
    </li>
  )
}

export function AreaDirectoryView({ areaId }: { areaId: AreaId }) {
  const { t } = useTranslation(['nav', 'common'])
  const { navigable } = useViewAccess()
  const area = areaById(areaId)
  if (!area) return null
  const label = areaLabel(t, areaId)

  const sections = authorizedSections(areaId, navigable).map((s) => ({
    sectionId: s.sectionId,
    entries: s.views.map((v): DirectoryEntry => ({
      id: v.id,
      path: v.path,
      icon: v.icon,
      label: viewLabel(t, v.id),
      description: viewDescription(t, v.id),
    })),
  }))
  // The Settings utility has no permission and no registry entry; it is the Preferences
  // section of System & settings, always — a principal with nothing else in the area still
  // has this one real door, so the empty state is not what they get.
  if (areaId === SETTINGS_UTILITY.areaId) {
    sections.push({
      sectionId: SETTINGS_UTILITY.sectionId,
      entries: [
        {
          id: SETTINGS_UTILITY.id,
          path: SETTINGS_UTILITY.path,
          icon: SETTINGS_UTILITY.icon,
          label: viewLabel(t, SETTINGS_UTILITY.id),
          description: viewDescription(t, SETTINGS_UTILITY.id),
        },
      ],
    })
    // Keep the area's declared section order (Preferences is last) even though the utility
    // was appended after the registry-derived sections.
    sections.sort(
      (a, b) =>
        area.sections.indexOf(a.sectionId) - area.sections.indexOf(b.sectionId),
    )
  }

  const AreaIcon = area.icon
  return (
    <div
      className="flex flex-col gap-4"
      data-slot="area-directory"
      data-area={areaId}
    >
      <PageHeader
        icon={area.icon}
        title={label}
        description={areaQuestion(t, areaId)}
      />
      {sections.length === 0 ? (
        <EmptyState
          icon={<AreaIcon />}
          title={t('nav:directory.empty')}
          description={t('nav:directory.emptyHint')}
          action={
            <Button asChild variant="secondary" size="sm">
              <Link to="/">{t('nav:directory.backHome')}</Link>
            </Button>
          }
        />
      ) : (
        sections.map((s) => {
          const headingId = `area-${areaId}-section-${s.sectionId}`
          return (
            <section
              key={s.sectionId}
              aria-labelledby={headingId}
              className="flex flex-col gap-3"
            >
              <h2
                id={headingId}
                // The same step the rail gives this same list of sections, so the
                // directory and the sidebar read as one structure rather than two.
                className="text-overline text-muted-foreground uppercase"
              >
                {sectionLabel(t, areaId, s.sectionId)}
              </h2>
              <ul className="flex flex-col">
                {s.entries.map((entry) => (
                  <EntryRow key={entry.id} entry={entry} />
                ))}
              </ul>
            </section>
          )
        })
      )}
    </div>
  )
}
