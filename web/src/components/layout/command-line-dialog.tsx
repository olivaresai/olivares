// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The command line, for every signed-in person (Root, 09b real use: a new user found no way
// from the console to the olivares CLI; the install line lived only in the administrator's
// setup wizard). Three steps, each one command filled with THIS engine's address, the one
// the browser is on. The syntax is the CLI's own, checked by CLX on 09b: `login` asks for
// the email and password at the terminal, so no credential is ever shown here; the pin
// is the certificate's public fingerprint, not a secret.
import { useTranslation } from 'react-i18next'
import { useServerInfo } from '@/lib/hooks/use-server-info'
import { CodeLine } from '@/components/ui/code-line'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'

export const INSTALL_COMMAND =
  'curl -fsSL https://olivares.ai/olivares/install.sh | sh'

/** The three commands for an engine at `origin` (scheme, host and port, no path). The CLI
 * trusts the engine by the pin the engine publishes (server-info `tls_pin_sha256`, CLX), or,
 * when it publishes none, by a copy of its certificate. */
export function commandLineSteps(
  origin: string,
  firstTask: string,
  pin?: string,
) {
  return [
    { id: 'install', hint: 'install.hint', command: INSTALL_COMMAND },
    pin
      ? {
          id: 'signIn',
          hint: 'signIn.hintPin',
          command: `olivares login --server ${origin} --pin-sha256 ${pin}`,
        }
      : {
          id: 'signIn',
          hint: 'signIn.hint',
          command: `olivares login --server ${origin} --ca-cert tls.crt`,
        },
    {
      id: 'start',
      hint: 'start.hint',
      command: `olivares session start . "<${firstTask}>"`,
    },
  ] as const
}

export function CommandLineDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation('auth')
  const info = useServerInfo()
  const steps = commandLineSteps(
    window.location.origin,
    t('commandLine.start.task'),
    info.data?.tls_pin_sha256 || undefined,
  )
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-xl" data-slot="command-line">
        <DialogHeader>
          <DialogTitle>{t('commandLine.title')}</DialogTitle>
          <DialogDescription>{t('commandLine.description')}</DialogDescription>
        </DialogHeader>
        <ol className="flex min-w-0 flex-col gap-4">
          {steps.map((step) => (
            <li
              key={step.id}
              className="flex min-w-0 flex-col gap-1.5"
              data-step={step.id}
            >
              <h3 className="text-body font-semibold text-text">
                {t(`commandLine.${step.id}.title`)}
              </h3>
              <p className="text-caption text-text-2">
                {t(`commandLine.${step.hint}`)}
              </p>
              <CodeLine command={step.command} label={null} />
            </li>
          ))}
        </ol>
      </DialogContent>
    </Dialog>
  )
}
