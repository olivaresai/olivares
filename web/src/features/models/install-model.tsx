// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// INSTALL A LOCAL MODEL, from the Models page. Downloading a model by name used to exist
// only inside the AI tools panel; here the person picks one from a short list or types
// its name, follows Ollama's own progress, can cancel, and sees what is installed with its
// size. The control is the one the AI tools panel uses (agent-tools/model-installer).
import { Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { AsyncSection, SectionCard } from '@/features/_intel'
import {
  InstalledModels,
  ModelInstaller,
} from '@/features/agent-tools/model-installer'
import { useOllama } from '@/features/agent-tools/use-ollama'
import { useAuth } from '@/lib/auth/context'

export function InstallModel() {
  const { t } = useTranslation('agentTools')
  // The Ollama routes answer a system administrator only: anyone else is not asked.
  const { isSuperadmin } = useAuth()
  const ollama = useOllama(isSuperadmin)
  if (!isSuperadmin) return null
  return (
    <SectionCard
      title={t('ollama.installTitle')}
      description={t('ollama.installDescription')}
    >
      <AsyncSection query={ollama.status} skeletonHeight={96}>
        {(s) =>
          s.installed ? (
            <div className="flex flex-col gap-4">
              <ModelInstaller ollama={ollama} />
              <InstalledModels ollama={ollama} />
            </div>
          ) : (
            <p className="text-body text-muted-foreground">
              {t('ollama.installMissing')}{' '}
              <Link
                to={'/agent-tools' as never}
                className="text-accent-text underline"
              >
                {t('ollama.installMissingAction')}
              </Link>
            </p>
          )
        }
      </AsyncSection>
    </SectionCard>
  )
}
