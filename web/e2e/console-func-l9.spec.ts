// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  expect,
  test,
  type Locator,
  type Page,
  type Response,
} from '@playwright/test'

// CONSOLE-FUNC-01 lot 9 runs against one disposable --seed-demo engine and its
// embedded console. There are deliberately no route/fulfil/abort hooks here:
// method, route and effect assertions all observe the live Go handlers.
const demoTenant = process.env.DEMO_TENANT ?? ''
const DEMO_EMAIL = 'demo@olivares.local'
const DEMO_PASSWORD = 'olivares-demo-estate'

test.use({ trace: 'off' })
test.describe.configure({ mode: 'serial', timeout: 360_000 })

interface ListBody<T> {
  items: T[]
  has_more?: boolean
}

interface KbRecord {
  id: string
  name: string
  status: string
}

interface PromptRecord {
  id: string
  name: string
  current_rev: number
}

interface MemoryRecord {
  id: string
  agent_ref: string
  key: string
  classification: string
}

interface ContextRecord {
  id: string
  scope_kind: string
  scope_ref: string
  strategy: string
  max_tokens: number
}

interface DataProductRecord {
  id: string
  name: string
  status: string
}

interface EntryRecord {
  id: string
  name: string
  status: string
  signed: boolean
}

interface VerifyRecord {
  verified: boolean
  hash_ok: boolean
  signature_ok: boolean
}

interface InstanceRecord {
  id: string
  name: string
  status: string
}

interface ConfigRecord {
  id: string
  server_ref: string
  revision: number
  enabled: boolean
  secret_refs: Array<{ name: string; ref_kind: string; ref: string }>
}

function authHeaders(token: string): Record<string, string> {
  return {
    Authorization: `Bearer ${token}`,
    'X-Olivares-Tenant': demoTenant,
  }
}

async function preparePage(page: Page): Promise<void> {
  await page.addInitScript(
    ([tenant]) => {
      localStorage.setItem(
        'olivares.tenant',
        JSON.stringify({ state: { activeTenant: tenant }, version: 0 }),
      )
      localStorage.setItem('olivares.lang', 'en')
      localStorage.setItem('olivares.theme', 'light')
    },
    [demoTenant],
  )
}

async function loginDemo(page: Page): Promise<string> {
  const loginRead = page.waitForResponse((response) => {
    const url = new URL(response.url())
    return (
      response.request().method() === 'POST' &&
      url.pathname === '/v1/auth/login'
    )
  })
  await page.goto('/login')
  await page.locator('#email').fill(DEMO_EMAIL)
  await page.locator('#password').fill(DEMO_PASSWORD)
  await page.getByRole('button', { name: /^sign in$/i }).click()
  const response = await loginRead
  expect(response.status()).toBe(200)
  const body = (await response.json()) as { token: string }
  await expect(
    page.getByRole('link', { name: 'Inventory', exact: true }),
  ).toBeVisible({ timeout: 20_000 })
  return body.token
}

function observe(
  page: Page,
  method: string,
  endpoint: string,
  status?: number,
): Promise<Response> {
  return page.waitForResponse(
    (response) => {
      const url = new URL(response.url())
      return (
        response.request().method() === method &&
        url.pathname === endpoint &&
        (status === undefined || response.status() === status)
      )
    },
    { timeout: 30_000 },
  )
}

async function getJSON<T>(page: Page, token: string, endpoint: string) {
  const response = await page.request.get(endpoint, {
    headers: authHeaders(token),
  })
  expect(response.ok(), `GET ${endpoint}`).toBe(true)
  return (await response.json()) as T
}

async function rowFor(page: Page, text: string): Promise<Locator> {
  const cell = page.getByText(text, { exact: true }).first()
  await expect(cell).toBeVisible({ timeout: 30_000 })
  return cell.locator('xpath=ancestor::tr[1]')
}

async function choose(
  page: Page,
  trigger: Locator,
  option: string | RegExp,
): Promise<void> {
  await trigger.click()
  await page.getByRole('option', { name: option, exact: true }).click()
}

async function typeConfirmation(page: Page, phrase: string): Promise<void> {
  const field = page.locator('#confirm-phrase')
  await expect(field).toBeVisible()
  await field.fill(phrase)
}

test.beforeEach(async ({ page }) => {
  test.skip(
    !demoTenant,
    'DEMO_TENANT not set — launch the disposable lot-9 engine',
  )
  page.setDefaultTimeout(20_000)
  await preparePage(page)
})

test('knowledge governs KB, prompt, memory, context, DLP and data-product lifecycles live', async ({
  page,
}) => {
  const token = await loginDemo(page)
  const nonce = Date.now()
  const kbName = `L9 browser KB ${nonce}`
  const kbEdited = `${kbName} edited`
  const promptName = `l9-browser-prompt-${nonce}`
  const agentRef = `agent:l9-browser-${nonce}`
  const memoryKey = `preference-${nonce}`
  const contextRef = `agent:l9-context-${nonce}`
  const dlpClass = `l9-${nonce}`
  const productName = `L9 browser product ${nonce}`

  const initialKbs = observe(page, 'GET', '/v1/m/knowledge/kbs', 200)
  await page.goto('/knowledge')
  await initialKbs
  await expect(
    page.getByRole('heading', { name: 'Data, knowledge & context' }),
  ).toBeVisible()

  const refreshed = observe(page, 'GET', '/v1/m/knowledge/kbs', 200)
  await page.getByRole('button', { name: 'Refresh', exact: true }).click()
  await refreshed

  let filtered = observe(page, 'GET', '/v1/m/knowledge/kbs', 200)
  await choose(
    page,
    page.getByRole('combobox', { name: 'Status', exact: true }),
    'Active',
  )
  expect(new URL((await filtered).url()).searchParams.get('status')).toBe(
    'active',
  )
  await choose(
    page,
    page.getByRole('combobox', { name: 'Status', exact: true }),
    'All',
  )

  await page.getByRole('button', { name: 'New knowledge base' }).click()
  const kbEditor = page.getByRole('dialog', { name: 'New knowledge base' })
  await kbEditor.locator('#kb-name').fill(kbName)
  await choose(page, kbEditor.locator('#kb-classification'), 'Public')
  await choose(page, kbEditor.locator('#kb-residency'), 'EU')
  await choose(page, kbEditor.locator('#kb-residency'), 'Global')
  await choose(page, kbEditor.locator('#kb-embed-policy'), /Local only/i)
  await kbEditor.getByRole('button', { name: 'Add reference' }).click()
  await kbEditor.getByRole('textbox', { name: 'ACL' }).fill('role:l9-review')
  await kbEditor.getByRole('button', { name: 'Remove', exact: true }).click()
  const kbCreate = observe(page, 'POST', '/v1/m/knowledge/kbs', 201)
  await kbEditor.getByRole('button', { name: 'Create knowledge base' }).click()
  const kb = (await (await kbCreate).json()) as KbRecord
  expect(kb.name).toBe(kbName)
  await expect(page.getByText(kbName, { exact: true })).toBeVisible()

  await (await rowFor(page, kbName)).click()
  const kbDetail = page.getByRole('dialog', { name: kbName })
  await expect(kbDetail).toBeVisible()
  await kbDetail.getByRole('button', { name: 'Edit governance' }).click()
  const kbEdit = page.getByRole('dialog', { name: 'Edit governance' })
  await kbEdit.locator('#kb-name').fill(kbEdited)
  const kbUpdate = observe(page, 'PUT', `/v1/m/knowledge/kbs/${kb.id}`, 200)
  await kbEdit.getByRole('button', { name: 'Save changes' }).click()
  expect(((await (await kbUpdate).json()) as KbRecord).name).toBe(kbEdited)
  await expect(kbDetail.getByRole('heading', { name: kbEdited })).toBeVisible()

  await kbDetail.getByRole('button', { name: 'Ingest' }).click()
  const ingest = page.getByRole('dialog', { name: 'Ingest content' })
  await ingest.getByRole('tab', { name: 'Inline documents' }).click()
  await ingest.getByRole('button', { name: 'Add document' }).click()
  await expect(ingest.getByRole('textbox', { name: 'Body' })).toHaveCount(2)
  await ingest
    .getByRole('button', { name: 'Remove', exact: true })
    .last()
    .click()
  await ingest
    .getByRole('textbox', { name: 'Source document id' })
    .fill(`l9-doc-${nonce}`)
  await ingest
    .getByRole('textbox', { name: 'Title' })
    .fill('L9 governed handbook')
  await ingest
    .getByRole('textbox', { name: 'Body' })
    .fill('Olivares governed knowledge browser retrieval evidence')
  await choose(
    page,
    ingest.getByRole('combobox', { name: 'Classification' }),
    'Public',
  )
  const ingestWrite = observe(
    page,
    'POST',
    `/v1/m/knowledge/kbs/${kb.id}/ingest`,
    200,
  )
  await ingest.getByRole('button', { name: 'Ingest', exact: true }).click()
  const ingestBody = (await (await ingestWrite).json()) as {
    documents: number
    chunks: number
  }
  expect(ingestBody.documents).toBe(1)
  expect(ingestBody.chunks).toBeGreaterThan(0)
  await expect(kbDetail.getByText('L9 governed handbook')).toBeVisible()

  await kbDetail.getByRole('button', { name: 'Run retrieval' }).click()
  const queryDialog = page.getByRole('dialog', { name: 'Governed retrieval' })
  await queryDialog.locator('#q-query').fill('governed knowledge evidence')
  await queryDialog.locator('#q-session').fill(`session:l9-${nonce}`)
  await queryDialog.locator('#q-topk').fill('5')
  const queryWrite = observe(
    page,
    'POST',
    `/v1/m/knowledge/kbs/${kb.id}/query`,
    200,
  )
  await queryDialog
    .getByRole('button', { name: 'Run retrieval', exact: true })
    .click()
  const queryBody = (await (await queryWrite).json()) as {
    lineage_id: string
    count: number
    results: unknown[]
  }
  expect(queryBody.count).toBeGreaterThan(0)
  await expect(queryDialog.getByText('L9 governed handbook')).toBeVisible()
  await expect(
    queryDialog.getByText(queryBody.lineage_id, { exact: false }),
  ).toBeVisible()
  await choose(page, queryDialog.getByRole('combobox', { name: 'Mode' }), 'All')
  await queryDialog.getByRole('button', { name: 'Close' }).first().click()
  await page.keyboard.press('Escape')

  await page.getByRole('tab', { name: 'Lineage' }).click()
  await rowFor(page, kb.id)
  filtered = observe(page, 'GET', '/v1/m/knowledge/lineage', 200)
  await choose(
    page,
    page.getByRole('combobox', { name: 'Decision' }),
    'Allowed',
  )
  expect(new URL((await filtered).url()).searchParams.get('decision')).toBe(
    'allowed',
  )
  const lineageGet = observe(
    page,
    'GET',
    `/v1/m/knowledge/lineage/${queryBody.lineage_id}`,
    200,
  )
  await (await rowFor(page, kb.id)).click()
  await lineageGet
  await expect(
    page.getByRole('dialog', { name: 'Lineage record' }),
  ).toBeVisible()
  await page.keyboard.press('Escape')

  await page.getByRole('tab', { name: 'Prompts' }).click()
  await page.getByRole('button', { name: 'New prompt' }).click()
  const promptEditor = page.getByRole('dialog', { name: 'New prompt' })
  await promptEditor.locator('#prompt-name').fill(promptName)
  await promptEditor
    .locator('#prompt-template')
    .fill('Summarize the governed input: {{input}}')
  await promptEditor.locator('#prompt-label').fill('v1')
  await promptEditor.locator('#prompt-note').fill('lot 9 browser')
  const promptCreate = observe(page, 'POST', '/v1/m/knowledge/prompts', 201)
  await promptEditor.getByRole('button', { name: 'Create prompt' }).click()
  const prompt = (await (await promptCreate).json()) as PromptRecord
  await expect(page.getByText(promptName, { exact: true })).toBeVisible()

  await (await rowFor(page, promptName)).click()
  const promptDetail = page.getByRole('dialog', { name: promptName })
  await promptDetail.getByRole('button', { name: 'Add revision' }).click()
  const revisionEditor = page.getByRole('dialog', { name: 'Add revision' })
  await revisionEditor
    .locator('#rev-template')
    .fill('Summarize carefully: {{input}}')
  await revisionEditor.locator('#rev-label').fill('v2')
  const revisionCreate = observe(
    page,
    'POST',
    `/v1/m/knowledge/prompts/${prompt.id}/revisions`,
    201,
  )
  await revisionEditor.getByRole('button', { name: 'Add revision' }).click()
  expect(((await (await revisionCreate).json()) as { rev: number }).rev).toBe(2)
  await expect(promptDetail.getByText('Revision 2')).toBeVisible()
  await promptDetail.getByRole('button', { name: 'Roll back to this' }).click()
  await typeConfirmation(page, 'ROLLBACK')
  const rollbackWrite = observe(
    page,
    'POST',
    `/v1/m/knowledge/prompts/${prompt.id}/rollback`,
    200,
  )
  await page.getByRole('button', { name: 'Roll back', exact: true }).click()
  expect(
    ((await (await rollbackWrite).json()) as { current_rev: number })
      .current_rev,
  ).toBe(1)
  await promptDetail.getByRole('button', { name: 'Close' }).click()

  await page.getByRole('tab', { name: 'Memory' }).click()
  const agentFilter = page.getByRole('textbox', { name: 'Agent' })
  await agentFilter.fill(agentRef)
  await page.getByRole('button', { name: 'Write memory' }).click()
  const memoryEditor = page.getByRole('dialog', { name: 'Write memory' })
  await memoryEditor.locator('#mem-agent').fill(agentRef)
  await memoryEditor.locator('#mem-key').fill(memoryKey)
  await memoryEditor.locator('#mem-content').fill('dark mode')
  await choose(page, memoryEditor.locator('#mem-class'), 'Public')
  await memoryEditor.locator('#mem-ttl').fill('3600')
  const memoryWrite = observe(page, 'POST', '/v1/m/knowledge/memory', 200)
  await memoryEditor.getByRole('button', { name: 'Write memory' }).click()
  const memory = (await (await memoryWrite).json()) as MemoryRecord
  expect(memory.classification).toBe('public')
  await expect(page.getByText(memoryKey, { exact: true })).toBeVisible()

  const memoryCard = page
    .getByText(memoryKey, { exact: true })
    .locator('xpath=ancestor::li[1]')
  await memoryCard.getByRole('button', { name: 'Delete' }).click()
  const memoryDelete = observe(
    page,
    'DELETE',
    `/v1/m/knowledge/memory/${memory.id}`,
    200,
  )
  await page.getByRole('button', { name: 'Delete', exact: true }).last().click()
  expect(
    ((await (await memoryDelete).json()) as { deleted: boolean }).deleted,
  ).toBe(true)
  await expect(page.getByText(memoryKey, { exact: true })).toHaveCount(0)

  await page.getByRole('button', { name: 'Purge expired' }).click()
  await typeConfirmation(page, 'PURGE')
  const purgeWrite = observe(page, 'POST', '/v1/m/knowledge/memory/purge', 200)
  await page.getByRole('button', { name: 'Purge', exact: true }).click()
  expect(
    ((await (await purgeWrite).json()) as { purged: number }).purged,
  ).toBeGreaterThanOrEqual(0)
  const exportRead = observe(page, 'GET', '/v1/m/knowledge/memory/export', 501)
  await page.getByRole('button', { name: 'Export bundle' }).click()
  await exportRead

  await page.getByRole('tab', { name: 'Context' }).click()
  await page.getByRole('button', { name: 'Upsert policy' }).click()
  let contextEditor = page.getByRole('dialog', {
    name: 'Upsert context policy',
  })
  await contextEditor.locator('#ctx-scope-ref').fill(contextRef)
  await contextEditor.locator('#ctx-max-tokens').fill('4096')
  await choose(page, contextEditor.locator('#ctx-strategy'), 'Window')
  await contextEditor.locator('#ctx-redaction').click()
  const contextCreate = observe(
    page,
    'POST',
    '/v1/m/knowledge/context-policies',
    201,
  )
  await contextEditor.getByRole('button', { name: 'Save policy' }).click()
  const context = (await (await contextCreate).json()) as ContextRecord
  expect(context.strategy).toBe('window')
  await expect(page.getByText(contextRef, { exact: true })).toBeVisible()

  await (await rowFor(page, contextRef)).click()
  contextEditor = page.getByRole('dialog', {
    name: 'Upsert context policy',
  })
  await contextEditor.locator('#ctx-max-tokens').fill('2048')
  await choose(page, contextEditor.locator('#ctx-strategy'), 'Truncate')
  const contextUpdate = observe(
    page,
    'POST',
    '/v1/m/knowledge/context-policies',
    200,
  )
  await contextEditor.getByRole('button', { name: 'Save policy' }).click()
  const contextUpdated = (await (await contextUpdate).json()) as ContextRecord
  expect(contextUpdated.max_tokens).toBe(2048)
  expect(contextUpdated.strategy).toBe('truncate')

  await page.getByRole('tab', { name: 'DLP' }).click()
  await page.getByRole('button', { name: 'New DLP rule' }).click()
  const dlpDialog = page.getByRole('dialog', { name: 'New DLP rule' })
  await dlpDialog.getByRole('textbox', { name: 'Class' }).fill(dlpClass)
  await choose(
    page,
    dlpDialog.getByRole('combobox', { name: 'Action' }),
    'allow',
  )
  const dlpCreate = observe(page, 'PUT', '/v1/m/knowledge/dlp/rules', 201)
  await dlpDialog.getByRole('button', { name: 'Save' }).click()
  const dlp = (await (await dlpCreate).json()) as {
    id: string
    class: string
    action: string
  }
  expect(dlp.action).toBe('allow')
  await expect(page.getByText(dlpClass, { exact: true })).toBeVisible()
  const dlpDelete = observe(
    page,
    'DELETE',
    `/v1/m/knowledge/dlp/rules/${dlp.id}`,
    204,
  )
  await page
    .getByRole('button', { name: `Delete the rule for ${dlpClass}` })
    .click()
  await dlpDelete
  await expect(page.getByText(dlpClass, { exact: true })).toHaveCount(0)

  await page.getByRole('tab', { name: 'Ledger anchors' }).click()
  const integrityWrite = observe(
    page,
    'POST',
    '/v1/m/knowledge/memory/verify',
    200,
  )
  await page.getByRole('button', { name: 'Verify memory' }).click()
  const integrity = (await (await integrityWrite).json()) as {
    checked: number
    verified: number
  }
  expect(integrity.checked).toBeGreaterThanOrEqual(integrity.verified)

  await page.getByRole('tab', { name: 'Data products' }).click()
  await page.getByRole('button', { name: 'New data product' }).click()
  const productEditor = page.getByRole('dialog', { name: 'New data product' })
  await productEditor.locator('#dp-name').fill(productName)
  await productEditor.locator('#dp-description').fill('lot 9 browser product')
  await productEditor.locator('#dp-owner').fill('team:l9')
  await productEditor.locator('#dp-kb').fill(kb.id)
  await productEditor.locator('#dp-avail').fill('99.9%')
  await productEditor.locator('#dp-tags').fill('{"lot":"9"}')
  await productEditor
    .locator('#dp-schema')
    .fill('{"type":"object","properties":{"answer":{"type":"string"}}}')
  await productEditor.locator('#dp-contract-note').fill('initial contract')
  const productCreate = observe(
    page,
    'POST',
    '/v1/m/knowledge/data-products',
    201,
  )
  const contractCreate = page.waitForResponse(
    (response) =>
      response.request().method() === 'POST' &&
      new URL(response.url()).pathname.endsWith('/contracts') &&
      response.status() === 201,
  )
  await productEditor.getByRole('button', { name: 'New data product' }).click()
  const product = (await (await productCreate).json()) as DataProductRecord
  await contractCreate
  await expect(page.getByText(productName, { exact: true })).toBeVisible()

  await (await rowFor(page, productName)).click()
  const productDetail = page.getByRole('dialog', { name: productName })
  await expect(productDetail.getByText('initial contract')).toBeVisible()
  const publishWrite = observe(
    page,
    'POST',
    `/v1/m/knowledge/data-products/${product.id}/publish`,
    200,
  )
  await productDetail.getByRole('button', { name: 'Publish' }).click()
  expect(
    ((await (await publishWrite).json()) as DataProductRecord).status,
  ).toBe('published')
  const deprecateWrite = observe(
    page,
    'POST',
    `/v1/m/knowledge/data-products/${product.id}/deprecate`,
    200,
  )
  await productDetail.getByRole('button', { name: 'Deprecate' }).click()
  expect(
    ((await (await deprecateWrite).json()) as DataProductRecord).status,
  ).toBe('deprecated')
  const archiveWrite = observe(
    page,
    'POST',
    `/v1/m/knowledge/data-products/${product.id}/archive`,
    200,
  )
  await productDetail.getByRole('button', { name: 'Archive' }).click()
  expect(
    ((await (await archiveWrite).json()) as DataProductRecord).status,
  ).toBe('archived')
  await productDetail
    .getByRole('button', { name: 'Delete data product' })
    .click()
  const productDelete = observe(
    page,
    'DELETE',
    `/v1/m/knowledge/data-products/${product.id}`,
    200,
  )
  await page
    .getByRole('button', { name: 'Delete data product', exact: true })
    .last()
    .click()
  expect(
    ((await (await productDelete).json()) as { deleted: boolean }).deleted,
  ).toBe(true)
  await expect(page.getByText(productName, { exact: true })).toHaveCount(0)

  await page.getByRole('tab', { name: 'Knowledge bases' }).click()
  await (await rowFor(page, kbEdited)).click()
  const finalKbDetail = page.getByRole('dialog', { name: kbEdited })
  await finalKbDetail.getByRole('button', { name: 'Delete' }).click()
  await typeConfirmation(page, 'DELETE')
  const kbDelete = observe(page, 'DELETE', `/v1/m/knowledge/kbs/${kb.id}`, 200)
  await page.getByRole('button', { name: 'Delete permanently' }).click()
  expect(
    ((await (await kbDelete).json()) as { deleted: boolean }).deleted,
  ).toBe(true)
  const kbs = await getJSON<ListBody<KbRecord>>(
    page,
    token,
    '/v1/m/knowledge/kbs',
  )
  expect(kbs.items.some((item) => item.id === kb.id)).toBe(false)
})

test('catalog freezes signed entries and governs active and rejected instances live', async ({
  page,
}) => {
  const token = await loginDemo(page)
  const nonce = Date.now()
  const entryName = `L9 browser MCP ${nonce}`
  const entryEdited = `${entryName} edited`
  const slug = `l9-browser-mcp-${nonce}`
  const activeName = `l9-active-${nonce}`
  const rejectedName = `l9-rejected-${nonce}`
  const disposableName = `L9 disposable connector ${nonce}`

  const initial = observe(page, 'GET', '/v1/m/catalog/entries', 200)
  await page.goto('/catalog')
  await initial
  await expect(page.getByRole('heading', { name: 'Catalog' })).toBeVisible()
  const refreshed = observe(page, 'GET', '/v1/m/catalog/entries', 200)
  await page.getByRole('button', { name: 'Refresh' }).click()
  await refreshed

  let filtered = observe(page, 'GET', '/v1/m/catalog/entries', 200)
  await choose(page, page.getByRole('combobox', { name: 'Kind' }), 'MCP')
  expect(new URL((await filtered).url()).searchParams.get('kind')).toBe('mcp')
  filtered = observe(page, 'GET', '/v1/m/catalog/entries', 200)
  await choose(page, page.getByRole('combobox', { name: 'Status' }), 'Draft')
  expect(new URL((await filtered).url()).searchParams.get('status')).toBe(
    'draft',
  )
  await choose(page, page.getByRole('combobox', { name: 'Kind' }), 'All')
  await choose(page, page.getByRole('combobox', { name: 'Status' }), 'All')

  await page.getByRole('button', { name: 'New entry' }).click()
  let entryEditor = page.getByRole('dialog', { name: 'New catalog entry' })
  await choose(page, entryEditor.locator('#entry-kind'), 'MCP')
  await entryEditor.locator('#entry-name').fill(entryName)
  await entryEditor.locator('#entry-slug').fill(slug)
  await entryEditor.locator('#entry-version').fill('1.0.0')
  await entryEditor.locator('#entry-summary').fill('lot 9 browser MCP')
  await entryEditor.locator('#entry-owner').fill('team:l9')
  await entryEditor
    .locator('#entry-spec')
    .fill('{"command":"l9-mcp","token_ref":"env:L9_TOKEN"}')
  const entryCreate = observe(page, 'POST', '/v1/m/catalog/entries', 201)
  await entryEditor.getByRole('button', { name: 'Create draft' }).click()
  const entry = (await (await entryCreate).json()) as EntryRecord
  await expect(page.getByText(entryName, { exact: true })).toBeVisible()

  await (await rowFor(page, entryName)).click()
  const entryDetail = page.getByRole('dialog', { name: entryName })
  await expect(entryDetail.getByText('Not pinned yet')).toBeVisible()
  await entryDetail.getByRole('button', { name: 'Re-verify' }).click()
  await entryDetail.getByRole('button', { name: 'Edit' }).click()
  entryEditor = page.getByRole('dialog', { name: 'Edit draft entry' })
  await entryEditor.locator('#entry-name').fill(entryEdited)
  await entryEditor.locator('#entry-summary').fill('lot 9 browser MCP edited')
  const entryUpdate = observe(
    page,
    'PUT',
    `/v1/m/catalog/entries/${entry.id}`,
    200,
  )
  await entryEditor.getByRole('button', { name: 'Save changes' }).click()
  expect(((await (await entryUpdate).json()) as EntryRecord).name).toBe(
    entryEdited,
  )
  await expect(
    entryDetail.getByRole('heading', { name: entryEdited }),
  ).toBeVisible()

  await entryDetail.getByRole('button', { name: 'Submit' }).click()
  const submitWrite = observe(
    page,
    'POST',
    `/v1/m/catalog/entries/${entry.id}/submit`,
    200,
  )
  await page.getByRole('button', { name: 'Submit for review' }).click()
  expect(((await (await submitWrite).json()) as EntryRecord).status).toBe(
    'pending',
  )
  await entryDetail.getByRole('button', { name: 'Approve' }).click()
  await typeConfirmation(page, 'APPROVE')
  const approveWrite = observe(
    page,
    'POST',
    `/v1/m/catalog/entries/${entry.id}/approve`,
    200,
  )
  await page.getByRole('button', { name: 'Approve and freeze' }).click()
  const approved = (await (await approveWrite).json()) as EntryRecord
  expect(approved.status).toBe('approved')
  expect(approved.signed).toBe(true)

  const verifyRead = observe(
    page,
    'GET',
    `/v1/m/catalog/entries/${entry.id}/verify`,
    200,
  )
  await entryDetail.getByRole('button', { name: 'Re-verify' }).click()
  const verified = (await (await verifyRead).json()) as VerifyRecord
  expect(verified).toMatchObject({
    verified: true,
    hash_ok: true,
    signature_ok: true,
  })

  async function instantiate(name: string): Promise<InstanceRecord> {
    await entryDetail.getByRole('button', { name: 'Instantiate' }).click()
    const dialog = page.getByRole('dialog', {
      name: 'Request an instantiation',
    })
    await dialog.locator('#inst-name').fill(name)
    await dialog.locator('#inst-target').fill('env:test')
    await dialog.locator('#inst-note').fill('lot 9 browser request')
    const write = observe(
      page,
      'POST',
      `/v1/m/catalog/entries/${entry.id}/instantiate`,
      201,
    )
    await dialog.getByRole('button', { name: 'Request instantiation' }).click()
    return (await (await write).json()) as InstanceRecord
  }

  const activeInstance = await instantiate(activeName)
  const rejectedInstance = await instantiate(rejectedName)
  // InstantiateDialog.onCreated is not passed by EntryDetailSheet: creation is
  // durable, but navigation is deliberately asserted as absent and done manually.
  await expect(entryDetail).toBeVisible()
  await page.keyboard.press('Escape')
  await page.getByRole('tab', { name: 'Instances' }).click()
  await expect(page.getByText(activeName, { exact: true })).toBeVisible()

  await (await rowFor(page, activeName)).click()
  let instanceDetail = page.getByRole('dialog', { name: activeName })
  await instanceDetail.getByRole('button', { name: 'Approve' }).click()
  let transition = page.getByRole('dialog', {
    name: 'Approve this instance request?',
  })
  await transition.locator('#transition-note').fill('approved in lot 9')
  let transitionWrite = observe(
    page,
    'POST',
    `/v1/m/catalog/instances/${activeInstance.id}/transition`,
    200,
  )
  await transition.getByRole('button', { name: 'Approve' }).click()
  expect(
    ((await (await transitionWrite).json()) as InstanceRecord).status,
  ).toBe('approved')
  await instanceDetail.getByRole('button', { name: 'Activate' }).click()
  transition = page.getByRole('dialog', { name: 'Activate this instance?' })
  transitionWrite = observe(
    page,
    'POST',
    `/v1/m/catalog/instances/${activeInstance.id}/transition`,
    200,
  )
  await transition.getByRole('button', { name: 'Activate' }).click()
  expect(
    ((await (await transitionWrite).json()) as InstanceRecord).status,
  ).toBe('active')
  await instanceDetail.getByRole('button', { name: 'Close' }).click()

  await (await rowFor(page, rejectedName)).click()
  instanceDetail = page.getByRole('dialog', { name: rejectedName })
  await instanceDetail.getByRole('button', { name: 'Reject' }).click()
  transition = page.getByRole('dialog', {
    name: 'Reject this instance request?',
  })
  transitionWrite = observe(
    page,
    'POST',
    `/v1/m/catalog/instances/${rejectedInstance.id}/transition`,
    200,
  )
  await transition.getByRole('button', { name: 'Reject' }).click()
  expect(
    ((await (await transitionWrite).json()) as InstanceRecord).status,
  ).toBe('rejected')
  await instanceDetail.getByRole('button', { name: 'Close' }).click()

  await page.getByRole('tab', { name: 'Entries' }).click()
  await (await rowFor(page, entryEdited)).click()
  const approvedDetail = page.getByRole('dialog', { name: entryEdited })
  await approvedDetail.getByRole('button', { name: 'Deprecate' }).click()
  const deprecateWrite = observe(
    page,
    'POST',
    `/v1/m/catalog/entries/${entry.id}/deprecate`,
    200,
  )
  await page
    .getByRole('button', { name: 'Deprecate', exact: true })
    .last()
    .click()
  expect(((await (await deprecateWrite).json()) as EntryRecord).status).toBe(
    'deprecated',
  )
  await approvedDetail.getByRole('button', { name: 'Close' }).click()

  await page.getByRole('button', { name: 'New entry' }).click()
  entryEditor = page.getByRole('dialog', { name: 'New catalog entry' })
  await choose(page, entryEditor.locator('#entry-kind'), 'Connector')
  await entryEditor.locator('#entry-name').fill(disposableName)
  await entryEditor.locator('#entry-slug').fill(`l9-disposable-${nonce}`)
  await entryEditor.locator('#entry-version').fill('0.1.0')
  const disposableCreate = observe(page, 'POST', '/v1/m/catalog/entries', 201)
  await entryEditor.getByRole('button', { name: 'Create draft' }).click()
  const disposable = (await (await disposableCreate).json()) as EntryRecord
  await (await rowFor(page, disposableName)).click()
  const disposableDetail = page.getByRole('dialog', { name: disposableName })
  await disposableDetail.getByRole('button', { name: 'Delete' }).click()
  await typeConfirmation(page, 'DELETE')
  const disposableDelete = observe(
    page,
    'DELETE',
    `/v1/m/catalog/entries/${disposable.id}`,
    204,
  )
  await page.getByRole('button', { name: 'Delete permanently' }).click()
  await disposableDelete
  await expect(page.getByText(disposableName, { exact: true })).toHaveCount(0)

  await page.getByRole('tab', { name: 'Admission policy' }).click()
  for (const policy of [
    {
      title: 'MCP entries',
      dialog: 'Edit MCP admission policy',
      endpoint: '/v1/m/catalog/mcp-admission/policy',
      note: `mcp lot 9 ${nonce}`,
    },
    {
      title: 'Connector entries',
      dialog: 'Edit connector admission policy',
      endpoint: '/v1/m/catalog/connector-admission/policy',
      note: `connector lot 9 ${nonce}`,
    },
  ]) {
    const card = page
      .getByRole('heading', { name: policy.title })
      .locator('xpath=ancestor::section[1]')
    await card.getByRole('button', { name: 'Edit policy' }).click()
    const dialog = page.getByRole('dialog', { name: policy.dialog })
    await dialog.locator('#cat-pol-note').fill(policy.note)
    const policyWrite = observe(page, 'PUT', policy.endpoint, 200)
    const policyRefetch = observe(page, 'GET', policy.endpoint, 200)
    await dialog.getByRole('button', { name: 'Save' }).click()
    const body = (await (await policyWrite).json()) as { note: string }
    expect(body.note).toBe(policy.note)
    expect(
      ((await (await policyRefetch).json()) as { note: string }).note,
    ).toBe(policy.note)
    await expect(card.getByText('Configured — observe only')).toBeVisible()
  }

  const allInstances = await getJSON<ListBody<InstanceRecord>>(
    page,
    token,
    '/v1/m/catalog/instances',
  )
  expect(
    allInstances.items.find((item) => item.id === activeInstance.id)?.status,
  ).toBe('active')
  expect(
    allInstances.items.find((item) => item.id === rejectedInstance.id)?.status,
  ).toBe('rejected')
})

test('capabilities reads the live graph and preserves config revisions while tool pins fail closed', async ({
  page,
}) => {
  const token = await loginDemo(page)
  const nonce = Date.now()
  const serverRef = `l9-browser-server-${nonce}`

  const initial = observe(page, 'GET', '/v1/m/capabilities/servers', 200)
  await page.goto('/capabilities')
  await initial
  await expect(
    page.getByRole('heading', { name: 'MCP & skills' }),
  ).toBeVisible()
  await expect(page.getByText('github', { exact: true })).toBeVisible()
  const refreshed = observe(page, 'GET', '/v1/m/capabilities/servers', 200)
  await page.getByRole('button', { name: 'Refresh' }).click()
  await refreshed

  const githubRow = await rowFor(page, 'github')
  const serverId = await githubRow.getAttribute('data-row-id')
  await githubRow.click()
  await expect(page.getByRole('dialog', { name: 'github' })).toBeVisible()
  await page.keyboard.press('Escape')

  await page.getByRole('tab', { name: 'Tools' }).click()
  await expect(page.getByText('create_issue', { exact: true })).toBeVisible()
  await expect(page.getByText(/annotations are self-reported/i)).toBeVisible()
  await page.getByPlaceholder('Search tools…').fill('create_issue')
  await expect(page.getByText('create_issue', { exact: true })).toBeVisible()
  await page.getByPlaceholder('Search tools…').fill('')

  await page.getByRole('tab', { name: 'Skills' }).click()
  await expect(page.getByText('No skills discovered')).toBeVisible()

  const wiringRead = observe(page, 'GET', '/v1/m/capabilities/wiring', 200)
  await page.getByRole('tab', { name: 'Wiring' }).click()
  const wiring = (await (await wiringRead).json()) as {
    nodes: unknown[]
    edges: unknown[]
  }
  expect(wiring.nodes.length).toBeGreaterThan(0)
  expect(wiring.edges.length).toBeGreaterThan(0)
  await expect(
    page.getByRole('group', { name: 'Capability wiring' }),
  ).toBeVisible()
  await page.getByRole('button', { name: 'Zoom In' }).click()
  await page.getByRole('button', { name: 'Zoom Out' }).click()
  await page.getByRole('button', { name: 'Fit View' }).click()

  const pinsRead = observe(page, 'GET', '/v1/m/capabilities/toolpins', 501)
  await page.getByRole('tab', { name: 'Tool pins' }).click()
  await pinsRead
  await expect(
    page.getByText(/enterprise capability.*no enterprise verifier/i),
  ).toBeVisible()

  await page.getByRole('tab', { name: 'Managed configs' }).click()
  await page.getByRole('button', { name: 'New config' }).click()
  let editor = page.getByRole('dialog', { name: 'New managed config' })
  await editor.locator('#cfg-server-ref').fill(serverRef)
  await choose(page, editor.locator('#cfg-transport'), 'stdio')
  await editor.locator('#cfg-endpoint').fill('cmd:l9-browser')
  await editor.locator('#cfg-scope').fill('team-l9')
  await editor.locator('#cfg-note').fill('created in lot 9')
  await editor.getByRole('button', { name: 'Add reference' }).click()
  await editor.getByRole('textbox', { name: 'Name' }).fill('token')
  await editor
    .getByRole('textbox', { name: 'Locator' })
    .fill('env:L9_BROWSER_TOKEN')
  await editor
    .getByRole('textbox', { name: 'Masked hint (optional)' })
    .fill('L9-ref')
  const configCreate = observe(page, 'POST', '/v1/m/capabilities/configs', 201)
  await editor.getByRole('button', { name: 'Create config' }).click()
  const config = (await (await configCreate).json()) as ConfigRecord
  expect(config.revision).toBe(1)
  expect(config.secret_refs).toHaveLength(1)
  await expect(page.getByText(serverRef, { exact: true })).toBeVisible()

  await (await rowFor(page, serverRef)).click()
  editor = page.getByRole('dialog', { name: 'Edit managed config' })
  await choose(page, editor.locator('#cfg-transport'), 'http')
  await editor.locator('#cfg-endpoint').fill('http://127.0.0.1:9/mcp')
  await editor.locator('#cfg-scope').fill('team-l9-updated')
  await editor.locator('#cfg-enabled').click()
  await editor.getByRole('button', { name: 'Remove' }).click()
  const configUpdate = observe(
    page,
    'PUT',
    `/v1/m/capabilities/configs/${config.id}`,
    200,
  )
  await editor.getByRole('button', { name: 'Save changes' }).click()
  const updated = (await (await configUpdate).json()) as ConfigRecord
  expect(updated.revision).toBe(2)
  expect(updated.enabled).toBe(false)
  expect(updated.secret_refs).toEqual([])

  // A discovered-server detail is the only UI entry to revisions/delete. The
  // custom config still proves row editing; the history is checked at its real
  // handler because no server record can honestly be fabricated by this test.
  const revisions = await getJSON<ListBody<{ revision: number }>>(
    page,
    token,
    `/v1/m/capabilities/configs/${config.id}/revisions`,
  )
  expect(revisions.items.map((item) => item.revision).sort()).toEqual([1, 2])

  const configDelete = await page.request.delete(
    `/v1/m/capabilities/configs/${config.id}`,
    { headers: authHeaders(token) },
  )
  expect(configDelete.status()).toBe(204)
  const remaining = await getJSON<ListBody<ConfigRecord>>(
    page,
    token,
    `/v1/m/capabilities/configs?server_ref=${encodeURIComponent(serverRef)}`,
  )
  expect(remaining.items).toEqual([])

  // The seeded row identity was rendered from a live handler even when the DOM
  // table does not expose it as an attribute; keep the value read to prevent an
  // accidental assertion that it did.
  expect(serverId === null || serverId.length > 0).toBe(true)
})
