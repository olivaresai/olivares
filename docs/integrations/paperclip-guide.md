<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Govern Paperclip model calls with the Olivares AI inference proxy

[Paperclip](https://github.com/paperclipai/paperclip) (MIT) runs teams of AI agents and starts the
agent tools itself. You can put the model calls of its `claude_local` agents under Olivares AI
governance without changing Paperclip: point each agent at the Olivares inference proxy instead of
the vendor API, with a credential from a proxy device grant. Every model call then passes the
proxy's gates (model access, DLP, budget) and is recorded in the tamper-evident audit ledger.

Measured with Olivares AI 26.10.2<!-- release-fixed --> and Paperclip 2026.1001.0 (npm `paperclipai`), whose
`claude_local` adapter runs Claude Code through `@agentclientprotocol/claude-agent-acp` 0.73.0.

## What this covers and what it does not

| Covered | Not covered |
|---|---|
| Anthropic Messages calls (`/v1/messages`) of Paperclip agents that use the `claude_local` adapter with an API key | Tool calls, files and commands of those agents |
| Model-access, DLP and budget gates before each call | Confinement of the agent process |
| Ledger rows for each authorized call, attributed to the proxy credential, and for each budget refusal | Paperclip's own task board, approvals and cost records |
| A budget that refuses calls before they reach the provider | Agents that use other adapters or a Claude subscription login |

Paperclip still starts the agent tool itself. On this path Olivares AI does not confine the
process and its tool-call policy point does not see the agent's tool calls. If you need that, run
the agent as a governed Olivares AI session instead.

Do not install host-wide Claude Code managed settings (`olivares agent managed-settings`) on the
host where Paperclip starts Claude Code. That hook refuses every tool call that does not come from
an Olivares AI session, so it would block every Paperclip agent.

## Prerequisites

- A running Olivares AI engine and an administrator sign-in for the CLI (`olivares login`).
- A Paperclip instance. A local trial is `npx paperclipai onboard --yes`, which uses embedded
  PostgreSQL and listens on `127.0.0.1:3100`.
- A `claude_local` agent in a Paperclip company.
- Network access from the Paperclip host to the proxy listener.

## 1. Turn on the inference proxy

The proxy is opt-in. It needs a configuration file, a credential source for device grants and the
`inferenceproxy` module.

Write the configuration file, readable only by the engine's service account:

```json
{
  "listen": "127.0.0.1:8448",
  "surface": "direct",
  "upstream_key": "<your Anthropic API key>",
  "public_url": "https://127.0.0.1:8448"
}
```

- `upstream_key` is the operator's vendor key. The proxy uses it for every forwarded call and
  never passes on the caller's credential.
- `public_url` is the address clients use. It turns on the device-grant endpoints.
- The listener serves TLS with the engine's certificate. With the self-signed certificate of a
  first boot, clients need the engine CA, `<data-dir>/tls.crt.ca`. This guide uses a copy of it at
  `/etc/olivares/tls.crt.ca` on the Paperclip host.

Issue the token that device grants will hand out, bound to one organization with the lowest role
(`viewer`):

```sh
olivares tokens issue --name paperclip-agents --role viewer -o json
```

This is an ordinary Olivares AI API token, not a proxy-only credential. Besides model calls, it
authenticates the Olivares AI API with everything the `viewer` role reads in its organization,
module data and the audit ledger included. Paperclip puts it in the agent's environment, and the
agent process is not confined, so any Paperclip agent, including a prompt-injected one, can read
that organization. Limit the reach in one of two ways:

- Bind the token to a dedicated organization for Paperclip agents
  (`olivares tenants create --name paperclip`, then `tokens issue --tenant <id>`). Proxy calls
  then run in that organization, so add `--tenant <id>` to every command of steps 3, 4 and 6.
  Without it they write to and read your own organization, and Paperclip calls get neither the
  DLP rules nor the budget. Your sign-in needs a role in the new organization;
  `tenants create` prints the `olivares members grant` command for it.
- Run Paperclip on a host that can reach the proxy listener but not the engine API.

The secret is printed once. Write it to a file readable only by the engine's service account.
Name that file in `OLIVARES_SESSION_RUNTIME_TOKEN_FILE` and the configuration file in
`OLIVARES_INFERENCE_PROXY_CONFIG`. On this engine the same file is also the inference credential
of governed sessions, so their model calls are attributed to `paperclip-agents` too and fall
under the same organization's DLP rules and budget.
Restart the engine, then turn on the module:

```sh
olivares modules on inferenceproxy
olivares modules ls
```

The engine restarts once and logs `inference-proxy: inline /v1/messages PEP mounted`.

## 2. Get a credential through a device grant

Start a grant on the proxy. The response carries a `user_code` to approve and a `device_code` to
redeem:

```sh
curl --cacert tls.crt.ca -X POST https://127.0.0.1:8448/oauth/device_authorization
```

An administrator approves the user code:

```sh
olivares inference-proxy device approve --user-code BWRR-FSTV --yes
```

Redeem the device code once (every 5 seconds until approved). Keep the device code in a file so it
does not show in the process list:

```sh
curl --cacert tls.crt.ca -X POST https://127.0.0.1:8448/oauth/token \
  --data-urlencode grant_type=urn:ietf:params:oauth:grant-type:device_code \
  --data @device-code.form
```

`device-code.form` contains `device_code=<value>`. Before approval the answer is
`authorization_pending`; after approval it is the `access_token`; a second redemption is
`invalid_grant`.

With the token-file source the `access_token` is the token from step 1. The `expires_in` value
(900 seconds by default, `OLIVARES_SESSION_RUNTIME_TOKEN_TTL`) is not its lifetime: the token
works until you rotate or revoke it (`olivares tokens rotate`, `olivares tokens revoke`). After
a rotation, run a new grant and update the Paperclip secret.

If `OLIVARES_SESSION_RUNTIME_WIF` is on, it takes precedence over the token file: the grant mints
a short-lived credential, and a Paperclip secret holding it stops working after `expires_in`.

## 3. Check the DLP rules

The stock request DLP lets Claude Code requests through. It refuses requests that contain
credentials (the `secret` class) and content the proxy cannot scan (the `unscanned` class). If
you add rules for the organization, also write the `*` rule: once an organization has its own
rules, a class without a rule is refused.

```sh
olivares inference-proxy dlp set --data '{"class":"*","action":"allow"}'
olivares inference-proxy dlp ls
```

A DLP refusal is not a ledger row. It is logged by the engine, and with the `security` module on
(`olivares modules on security`) it is also a finding (`olivares findings export`).

## 4. Set a budget

Create an enforcing budget on the proxy's gateway. Set `enabled` explicitly; a budget created
without it does not enforce.

```sh
# Budget authoring requires Business; stored caps remain enforced in Community.
olivares finops budgets create --data '{
  "name": "paperclip-proxy", "enabled": true,
  "dimension": "gateway", "key": "direct",
  "limit_micro_usd": 50000000, "period": "monthly", "action": "block"
}'
```

To limit only the calls of this credential, use the `actor` dimension with the token's actor as
the key (`"dimension": "actor", "key": "token:<token-id>"`, the `ACTOR` column of the ledger rows
in step 6). The `model` dimension limits the calls to one model.

Before each call the proxy holds an estimate against the budget: 0.01 USD plus 0.00001 USD per
token of the request's `max_tokens`. Claude Code asks for 64,000 tokens, so each of its calls holds
0.65 USD until it settles at the measured cost. A call is refused when the remaining budget cannot
cover its hold, so an agent stops before the last 0.65 USD is spent.

## 5. Point the Paperclip agent at the proxy

Store the credential in Paperclip's secret store. `--value-env` reads it from an environment
variable, so it stays out of the command line:

```sh
OLIVARES_PROXY_KEY="$(cat paperclip-proxy.key)" npx paperclipai secrets create \
  -C <company-id> --name "Olivares inference proxy key" --key OLIVARES_PROXY_KEY \
  --value-env OLIVARES_PROXY_KEY
```

Set the agent's `adapterConfig.env` (in the agent's configuration page, or with
`paperclipai agent create` / `agent update`):

```json
{
  "ANTHROPIC_BASE_URL": { "type": "plain", "value": "https://127.0.0.1:8448" },
  "ANTHROPIC_API_KEY": { "type": "secret_ref", "secretId": "<secret-id>", "version": "latest" },
  "NODE_EXTRA_CA_CERTS": { "type": "plain", "value": "/etc/olivares/tls.crt.ca" },
  "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": { "type": "plain", "value": "1" }
}
```

- Paperclip resolves the secret reference when it starts the agent and passes these variables to
  Claude Code.
- `NODE_EXTRA_CA_CERTS` is needed only while the engine uses its self-signed certificate.
- `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` stops Claude Code's telemetry and update traffic,
  which does not go through the proxy.
- Paperclip passes its own server environment to the agents it starts. Keep vendor API keys out of
  that environment, or an agent can reach the vendor without the proxy.

## 6. Check it

Run a heartbeat from Paperclip (the agent's **Run** action, or
`npx paperclipai agent heartbeat:invoke <agent-id>`). Then read the ledger and the budget:

```sh
olivares audit ls
olivares finops budgets status <budget-id>
```

Each model call adds two ledger rows for the proxy credential:

```text
ACTOR             ACTION                      TARGET
token:<token-id>  inference.proxy.recorded    inferenceproxy.call 37d34f6c…
token:<token-id>  inference.proxy.authorized  inferenceproxy.call 37d34f6c…
```

The budget status shows the settled spend. `olivares finops spend summary` breaks it down by
model.

## What a refusal looks like

| Refusal | Proxy answer | Ledger | Paperclip |
|---|---|---|---|
| Budget | `402 billing_error`, `budget limit reached`, `x-should-retry: false` | `finops.admission.denied` (actor `finops`) | The run fails: `ACP agent reported a terminal service failure.` |
| DLP | `403 permission_error`, `request blocked by data-loss-prevention policy` | none; a finding when the `security` module is on | The run fails: `ACP agent reported a terminal access failure.` |

Paperclip records the run as failed with a cost of 0 and does not show the reason. The reason is
in Olivares AI: the ledger, the findings and the engine log (`inference-proxy: decision`).

## Limits

- Only the model calls are governed. Paperclip starts the tool, so there is no Olivares AI
  confinement, tool-call policy or session record on this path.
- The Paperclip credential is an Olivares AI API token. It also reads its organization through
  the Olivares AI API; see step 1 for how to limit that.
- The context-window check sends the request to the provider's `count_tokens` endpoint before the
  budget gate runs. A call the budget refuses has already been sized upstream; the DLP and
  model-access gates run before that.
- The proxy serves the Anthropic Messages API (`direct` and `claude-platform-aws` surfaces).
  Paperclip agents that use other adapters, or Claude Code with a subscription login instead of an
  API key, are not governed by it.
