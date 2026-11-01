// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMutation } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { AuthShell } from '@/components/layout/auth-shell'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { authApi } from '@/lib/api/endpoints'
import { useAuth } from '@/lib/auth/context'
import '@/features/identity/i18n'

export function AcceptPairingPage() {
  const { t } = useTranslation('identity')
  const navigate = useNavigate()
  const { adoptSession } = useAuth()
  const [token] = useState(
    () => new URLSearchParams(window.location.hash.slice(1)).get('pair') ?? '',
  )
  useEffect(() => {
    window.history.replaceState(window.history.state, '', '/pair')
  }, [])
  const pairing = useMutation({
    mutationFn: () => authApi.acceptPairing(token),
    onSuccess: async (session) => {
      await adoptSession(session)
      void navigate({ to: '/', replace: true })
    },
  })
  return (
    <AuthShell>
      <Card className="flex flex-col gap-4 p-6">
        <h1 className="font-display text-title">{t('sessions.pairTitle')}</h1>
        <p className="text-body text-muted-foreground">
          {t('sessions.acceptHint')}
        </p>
        {!token || pairing.isError ? (
          <p role="alert" className="text-body text-danger">
            {t('sessions.pairInvalid')}
          </p>
        ) : null}
        <Button
          disabled={!token || pairing.isPending || pairing.isSuccess}
          onClick={() => pairing.mutate()}
        >
          {t('sessions.accept')}
        </Button>
      </Card>
    </AuthShell>
  )
}
