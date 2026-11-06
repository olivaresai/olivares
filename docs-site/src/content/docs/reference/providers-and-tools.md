---
title: Providers and tools
description: Which AI tool runs on which vendor endpoint, and the preset that sets it up. The table is generated from the provider catalog.
---

Pick your vendor in the table, use its preset, and paste the key. Olivares fills in the
provider kind, the endpoint and the name, so you do not look up the vendor's address or
work out which tools can use it.

DeepSeek with Claude Code, from the CLI:

```sh
# The key is read from stdin, never from a flag.
olivares provider add --preset deepseek-anthropic < key.txt
```

The command registers the key, tests it with one model-list call (nothing is spent) and
prints the result. In the console, open **Tools**, find **API keys**, select **Add an API
key**, choose the vendor and paste the key. Then bind the provider to a profile and start
a session as described in [Add a provider](/how-to/add-a-provider/).

## Presets

<!-- BEGIN GENERATED olivares-providers-tools — regenerate with `bash scripts/check-guide-docs.sh --write`; do not edit by hand -->

| Vendor | Preset | Runs in | Endpoint | Auth | Links |
|---|---|---|---|---|---|
| `DS` DeepSeek (Claude Code) | `deepseek-anthropic` | Claude Code (`claude`) | `https://api.deepseek.com/anthropic` | Bearer token | [Docs](https://api-docs.deepseek.com/quick_start/agent_integrations/claude_code), [Get a key](https://platform.deepseek.com/api_keys) |
| `Z` Z.ai | `zai` | OpenCode (`opencode`) | `https://api.z.ai/api/paas/v4` | Bearer token | [Docs](https://docs.z.ai/), [Get a key](https://z.ai/manage-apikey/apikey-list) |
| `Z` Z.ai (Claude Code) | `zai-anthropic` | Claude Code (`claude`) | `https://api.z.ai/api/anthropic` | Bearer token | [Docs](https://docs.z.ai/devpack/tool/claude), [Get a key](https://z.ai/manage-apikey/apikey-list) |
| `KI` Kimi Code | `kimi-code` | Claude Code (`claude`) | `https://api.kimi.ai/coding` | `x-api-key` header | [Docs](https://www.kimi.com/code/docs/en/) |
| `MO` Moonshot | `moonshot` | Codex (`codex`), OpenCode (`opencode`) | `https://api.moonshot.ai/v1` | Bearer token | [Docs](https://platform.kimi.ai/docs/guide/agent-support) |
| `MO` Moonshot (Claude Code) | `moonshot-anthropic` | Claude Code (`claude`) | `https://api.moonshot.ai/anthropic` | Bearer token | [Docs](https://platform.kimi.ai/docs/guide/claude-code-kimi) |
| `OR` OpenRouter | `openrouter` | Codex (`codex`), OpenCode (`opencode`) | `https://openrouter.ai/api/v1` | Bearer token | [Docs](https://openrouter.ai/docs/quickstart), [Get a key](https://openrouter.ai/settings/keys) |
| `OR` OpenRouter (Claude Code) | `openrouter-anthropic` | Claude Code (`claude`) | `https://openrouter.ai/api` | Bearer token | [Docs](https://openrouter.ai/docs/guides/guides/claude-code-integration), [Get a key](https://openrouter.ai/settings/keys) |
| `MI` Mistral | `mistral` | Codex (`codex`), OpenCode (`opencode`) | `https://api.mistral.ai/v1` | Bearer token | [Docs](https://docs.mistral.ai/), [Get a key](https://console.mistral.ai/api-keys) |
| `OC` OpenCode Zen (Claude Code) | `opencode-zen` | Claude Code (`claude`) | `https://opencode.ai/inference/anthropic` | Bearer token | [Docs](https://opencode.ai/v2/docs/console/harnesses/claude-code/) |
| `OC` OpenCode Go (Claude Code) | `opencode-go` | Claude Code (`claude`) | `https://opencode.ai/inference/go/anthropic` | Bearer token | [Docs](https://opencode.ai/v2/docs/console/harnesses/claude-code/) |

<!-- END GENERATED olivares-providers-tools -->

**Preset** is the value of `olivares provider add --preset`; tab completion lists the same
refs. **Runs in** lists the tools Olivares starts on that endpoint. It comes from the rule
the engine applies when it binds a provider to a tool, so a row never promises a pairing
that the engine refuses at launch. Codex appears only where the vendor serves the
Responses API, the only API Codex speaks. **Auth** is how the vendor wants the key, and
Olivares sends it that way. The links go to the vendor's own documentation and key page.

DeepSeek's OpenAI-style endpoint (preset `deepseek`) is for governed inference and starts
no tool session, so the table leaves it out.

A vendor that is not in the table still works with `--kind` and `--base-url`; see
[Add a provider](/how-to/add-a-provider/) for which tools read each kind. For what each
tool can do once it runs, see [Supported tools and integration depth](/reference/supported-tools/).
