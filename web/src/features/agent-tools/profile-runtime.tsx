// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The runtime of a Claude Code profile: the token count its sessions compact at. The engine
// sets Claude Code's own CLAUDE_CODE_AUTO_COMPACT_WINDOW from it on every launch; a session
// already running keeps the value it started with. Empty uses Claude's own default.
import { useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import type { ProviderAccountDTO } from '@/features/agentops/types'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import './i18n'

/** Claude Code's own range for the window, in tokens (the engine refuses the rest). */
const MIN_WINDOW = 100_000
const MAX_WINDOW = 1_000_000

/** The tokens typed, 0 for none (empty), or undefined when it is not a whole number in
 * the tool's range. */
function windowOf(text: string): number | undefined {
  const typed = text.trim()
  if (typed === '') return 0
  if (!/^[1-9]\d*$/.test(typed)) return undefined
  const tokens = Number(typed)
  return tokens >= MIN_WINDOW && tokens <= MAX_WINDOW ? tokens : undefined
}

export function ProfileRuntime({ account }: { account: ProviderAccountDTO }) {
  const { t } = useTranslation('agentTools')
  const { activeTenant } = useAuth()
  const boundary = useAuthBoundary()
  const heading = useId()
  const stored = account.auto_compact_window ?? 0
  const [text, setText] = useState(stored ? String(stored) : '')
  const tokens = windowOf(text)
  const changed = tokens !== stored
  const save = usePrivilegedMutation<number, unknown>({
    mutationFn: (value, authority) =>
      agentOpsApi.patchProfile(
        account.account_ref,
        { auto_compact_window: value },
        authority,
      ),
    successMessage: t('runtime.saved'),
    invalidateKeys: [
      agentOpsKeys.accounts(activeTenant, boundary.epoch),
      agentOpsKeys.profiles(activeTenant, boundary.epoch),
    ],
  })
  return (
    <section aria-labelledby={heading} className="flex flex-col gap-3">
      <h3 id={heading} className="text-body font-medium text-text">
        {t('runtime.title')}
      </h3>
      <Field
        label={t('runtime.autoCompact')}
        description={t('runtime.autoCompactHint')}
        error={
          tokens === undefined ? t('runtime.autoCompactInvalid') : undefined
        }
      >
        <Input
          value={text}
          onChange={(event) => setText(event.target.value)}
          inputMode="numeric"
          autoComplete="off"
          spellCheck={false}
          placeholder={t('runtime.autoCompactPlaceholder')}
        />
      </Field>
      {changed ? (
        <Button
          className="self-start"
          disabled={tokens === undefined || save.isPending}
          onClick={() => tokens !== undefined && save.mutate(tokens)}
        >
          {t('profiles.save')}
        </Button>
      ) : null}
    </section>
  )
}
