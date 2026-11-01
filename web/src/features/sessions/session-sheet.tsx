// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ONE SESSION IN A SHEET, OVER THE ESTATE (#2549, C2-IMPROVE E4). Opening a session from
// Estate keeps the estate in view: the sheet is the session page's own narrative, the
// header with Interrupt and Stop and the real conversation as the tool streams it, not a
// second telling of the session. "Open the session" goes to the full page, where the
// same conversation runs at full height beside the list and the Context pane.
//
// The address is the one the Sessions page reads (`session-address.ts`), held in the
// Estate's own `?session=`, so a reload or a shared link reopens the sheet and Back
// closes it.
import { Link } from '@tanstack/react-router'
import { ArrowUpRight } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Sheet, SheetContent, SheetTitle } from '@/components/ui/sheet'
import { operatorName, runTitle } from './provenance'
import {
  SESSION_PARAM,
  targetFromAddress,
  type EvidenceBlock,
} from './session-address'
import { SessionNarrative } from './session-narrative'
import type { SessionTarget } from './session-target'
import { useSessionResolution } from './use-session-resolution'
import './i18n'

function SheetBody({
  address,
  target,
}: {
  address: string
  target: SessionTarget
}) {
  const { t } = useTranslation('sessions')
  // Resolved once, here: the header, the conversation and the stream read this one
  // answer (`use-session-resolution.ts`).
  const resolution = useSessionResolution(target)
  const [evidence, setEvidence] = useState<EvidenceBlock | undefined>()

  return (
    <>
      {/* The name its operator typed; "Session" for one without, whose header tells it
          by the tail of its reference. */}
      <SheetTitle className="sr-only">
        {operatorName(resolution.session) ??
          runTitle(resolution.session) ??
          t('sheet.title')}
      </SheetTitle>
      {/* The way to the full page, on the line of the sheet's close button. */}
      <div className="flex h-12 shrink-0 items-center border-b border-line pl-3 pr-12">
        <Button asChild variant="ghost" size="sm">
          <Link
            to={'/sessions' as never}
            search={{ [SESSION_PARAM]: address } as never}
            data-testid="session-sheet-open"
          >
            {t('sheet.open')}
            <ArrowUpRight />
          </Link>
        </Button>
      </div>
      <SessionNarrative
        resolution={resolution}
        pinned={false}
        onTogglePin={null}
        evidence={evidence}
        onExpandEvidence={setEvidence}
        stacked
      />
    </>
  )
}

export function SessionSheet({
  address,
  onClose,
}: {
  /** The session to show, by its address (validated by the caller); null is closed. */
  address: string | null
  onClose: () => void
}) {
  // The session stays on the sheet while it slides out: the address is gone from the URL
  // the moment the person closes it, and an emptied panel would slide away blank.
  const [shown, setShown] = useState(address)
  if (address && address !== shown) setShown(address)
  const target = shown ? targetFromAddress(shown) : null
  return (
    <Sheet
      open={!!address && !!target}
      onOpenChange={(open) => !open && onClose()}
    >
      <SheetContent
        data-testid="session-sheet"
        // The session's name is the sheet's title; there is no separate description.
        aria-describedby={undefined}
        className="w-full max-w-[560px] gap-0 p-0"
      >
        {shown && target ? (
          // A new session is a new body: its evidence choice and stream start over.
          <SheetBody key={shown} address={shown} target={target} />
        ) : null}
      </SheetContent>
    </Sheet>
  )
}
