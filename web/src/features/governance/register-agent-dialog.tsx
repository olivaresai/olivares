// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { UserPlus } from 'lucide-react'
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
import { toast } from '@/components/ui/toaster'
import { useAuth } from '@/lib/auth/context'
import { useFailedActionReporter } from '@/lib/hooks/use-privileged-mutation'
import { governanceApi, governanceKeys } from './api'
import { AGENT_CRITICALITIES } from './types'

const SIN_CRITICIDAD = '__none__'

/** Registra una identidad de agente.
 *
 *  ⛔ EL PATROCINADOR NO ES OPCIONAL. El motor lo exige deny-closed —«a sponsor is mandatory
 *  for agent identities»— y además comprueba que esté en el roster y que sea una identidad
 *  HUMANA. Este formulario lo pide como obligatorio por la misma razón que el motor lo exige:
 *  tratarlo como opcional no relaja la regla, sólo traslada el rechazo al servidor y se lo
 *  enseña al operador como un error que no puede prever.
 *
 *  ⛔ Y EL RESULTADO NO ES SIEMPRE «CREADO». El motor contesta 201 si creó la fila y 200 si
 *  PROMOVIÓ una identidad que ya existía; no hay cuerpo, sólo el código. `registerAgent` lo
 *  traduce a `{promoted}` y aquí se dicen cosas distintas — decir «creado» ante una promoción
 *  afirmaría que apareció algo que ya estaba. */
export function RegisterAgentDialog({ canAdmin }: { canAdmin: boolean }) {
  const { t } = useTranslation(['governance', 'common'])
  const { activeTenant } = useAuth()
  const queryClient = useQueryClient()
  const report = useFailedActionReporter()
  const [open, setOpen] = useState(false)
  const [identityRef, setIdentityRef] = useState('')
  const [sponsorRef, setSponsorRef] = useState('')
  const [source, setSource] = useState('')
  const [criticality, setCriticality] = useState<string>(SIN_CRITICIDAD)

  const listo = identityRef.trim() !== '' && sponsorRef.trim() !== ''

  const mutation = useMutation({
    mutationFn: () =>
      governanceApi.registerAgent({
        identity_ref: identityRef.trim(),
        sponsor_ref: sponsorRef.trim(),
        ...(source.trim() ? { source: source.trim() } : {}),
        ...(criticality !== SIN_CRITICIDAD ? { criticality } : {}),
      }),
    onSuccess: (res) => {
      toast.success(
        res.promoted ? t('registerAgent.promoted') : t('registerAgent.created'),
      )
      void queryClient.invalidateQueries({
        queryKey: governanceKeys.identities(activeTenant),
      })
      setOpen(false)
      setIdentityRef('')
      setSponsorRef('')
      setSource('')
      setCriticality(SIN_CRITICIDAD)
    },
    // Los rechazos del motor son TEXTO ÚTIL —«el patrocinador no está en el roster; sincronízalo
    // primero», «no es una identidad humana», «ya registrado»—: se muestran, no se sustituyen
    // por un mensaje propio que perdería la instrucción.
    onError: (e) => report(e),
  })

  if (!canAdmin) return null

  return (
    <>
      <Button variant="primary" size="sm" onClick={() => setOpen(true)}>
        <UserPlus />
        {t('registerAgent.action')}
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('registerAgent.title')}</DialogTitle>
            <DialogDescription>{t('registerAgent.body')}</DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-4">
            <Field
              label={t('registerAgent.identityRef')}
              htmlFor="ra-identity"
              description={t('registerAgent.identityRefHint')}
            >
              <Input
                id="ra-identity"
                value={identityRef}
                onChange={(e) => setIdentityRef(e.target.value)}
              />
            </Field>
            <Field
              label={t('registerAgent.sponsorRef')}
              htmlFor="ra-sponsor"
              description={t('registerAgent.sponsorRefHint')}
            >
              <Input
                id="ra-sponsor"
                value={sponsorRef}
                onChange={(e) => setSponsorRef(e.target.value)}
              />
            </Field>
            <Field label={t('registerAgent.source')} htmlFor="ra-source">
              <Input
                id="ra-source"
                value={source}
                onChange={(e) => setSource(e.target.value)}
              />
            </Field>
            <Field
              label={t('registerAgent.criticality')}
              htmlFor="ra-criticality"
            >
              <Select value={criticality} onValueChange={setCriticality}>
                <SelectTrigger id="ra-criticality">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={SIN_CRITICIDAD}>
                    {t('registerAgent.noCriticality')}
                  </SelectItem>
                  {AGENT_CRITICALITIES.map((c) => (
                    <SelectItem key={c} value={c}>
                      {t(`registerAgent.crit.${c}`, { defaultValue: c })}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          </div>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setOpen(false)}>
              {t('common:actions.cancel')}
            </Button>
            <Button
              variant="primary"
              disabled={!listo || mutation.isPending}
              onClick={() => mutation.mutate()}
            >
              {t('registerAgent.submit')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
