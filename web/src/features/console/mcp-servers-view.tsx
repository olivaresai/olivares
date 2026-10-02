// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'
import { PageHeader } from '@/components/ui/page-header'
import { MCPGatewayTab } from './mcp-gateway-tab'

/** MCP servers beside AI tools: the tenant gateway that Administration > MCP gateway also
 * manages, at the place people look for it. */
export function MCPServersView() {
  const { t } = useTranslation('nav')
  return (
    <div className="flex min-w-0 flex-col gap-4">
      <PageHeader
        title={t('items.mcpServers')}
        description={t('descriptions.mcpServers')}
      />
      <MCPGatewayTab titled={false} />
    </div>
  )
}
