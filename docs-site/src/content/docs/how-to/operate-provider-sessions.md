---
title: Operate a provider session
description: >-
  Choose an AI tool and its account or provider, work in a project folder,
  approve actions, interrupt a turn, stop and resume from the console or CLI.
---

Complete [Your first hour](/how-to/first-hour/) before starting here.
Tools run on the engine's host, including inside the container for a Compose
installation. Your client terminal's login and folders are not automatically
available there.

## Choose how the tool connects

**AI tools** (`/agent-tools`) installs tools and runs their own login flows.
On a native engine, the default tool instance uses the engine user's own login;
accounts signed in through Olivares remain separate instances. Select the
account you want to use. The account snapshot shows the email, plan, usage
windows and models the tool reports. Missing values stay unavailable, and
failed refreshes are marked stale.

**Providers** (`/providers`) holds API keys and local-model endpoints. Add and
test the provider there, then use it from New session. A provider key and a tool
subscription are different ways to connect. Ollama needs a reachable endpoint
and a model, with no API key. See [Add a provider](/how-to/add-a-provider/).

For Claude profiles with `managed_injection` that name **no** provider,
`OLIVARES_SESSION_RUNTIME_WIF` or `OLIVARES_SESSION_RUNTIME_TOKEN_FILE` supplies
the host-wide inference credential. A profile bound to a Providers record uses
that record instead; if its credential cannot be opened, launch is refused
without falling back to the host credential. A `provider_account_home` profile
uses its authorized tool login and needs neither variable. See
[where the environment variables apply](/how-to/add-a-provider/#the-environment-variables-and-where-they-still-apply).

## Start and talk

Open **Sessions → New session** and choose a ready tool. The engine fills the
profile and folder. For the README Compose installation, choose **Change folder**
and enter `/project` to work on your mounted host project. Leaving the default
folder needs no typed path. Leave First message empty to start, then type in
the conversation, or enter it before pressing **Start**.

A tool using a provider key uses its saved model or lets you select a model
returned by the connection test. If the model is required but the test has not
returned any, test the connection in **Providers** first. Claude Code can use
its native default model.

For the README Compose installation, run the CLI inside the engine container.
It finds the local engine and its certificate there. Sign in with the Olivares
administrator account you created during setup, not your tool subscription.

When using a provider key with a tool that requires a model, such as Codex,
save a default in **Providers** before running `session start` below. If neither
the provider nor the tool's profile has a configured model, append
`--model <tested-model-id>` to the `session start` command, replacing the
placeholder with a model ID returned
by the connection test. A model selected in **New session** applies only to
that console launch; it does not set the model for a CLI launch.

Set the Compose file once in your host terminal:

```sh
export COMPOSE_FILE="$HOME/olivares/deploy/compose/docker-compose.yml"
docker compose exec olivares olivares login
docker compose exec olivares olivares tool ls
docker compose exec olivares olivares tool providers
docker compose exec olivares olivares session start /project --name first-hour
docker compose exec olivares olivares session send first-hour "Explain this project."
```

For a native installation, run the same Olivares commands directly on the
engine host and replace `/project` with your project path. The client prints
which tool and connection it chose. With several tools, select
one using `--tool`; `--profile` selects an existing profile. Closing the client
terminal leaves the session running on the engine. To reconnect to its output:

```sh
docker compose exec olivares olivares session follow first-hour
```

## Approve, interrupt, stop and resume

Under **More options**, **Ask before each action** makes the tool ask before
acting. In **Approvals**, inspect the pending request, choose **Approve** or
**Deny**, and confirm. A launch that requires an organization approval waits
there too; send your message once the launch is approved.

Use **Interrupt** to end the current turn while leaving the conversation open.
Use **Stop** to end the process, confirming if asked. **Resume** restarts that
same session on the same profile, account and folder; it rechecks credentials
and policy. Send a new message after resuming.

```sh
docker compose exec olivares olivares session interrupt first-hour
docker compose exec olivares olivares session stop first-hour
docker compose exec olivares olivares session resume first-hour
docker compose exec olivares olivares session send first-hour "Continue explaining this project."
```

A changed or unavailable account is a launch refusal, not permission to switch
to another login. An interrupted Grok turn uses ACP cancellation without an
acknowledgment; wait for the correlated turn result before treating it as ended.

## Optional profile and git settings

For an existing configured tool home, **Provider profiles** (`/provider-profiles`)
lets an administrator register a profile and inspect launch readiness.
The homes must exist on the execution host. Use **More options → Advanced launch
options** in New session to select that profile. Binary overrides choose an
executable; otherwise the engine tries the newest verified managed install,
then the tool on its `PATH`. See [Configuration](/reference/configuration/).

For a git repository's top folder with at least one commit, **Work in a new git
worktree** creates a branch and working directory for the session. Resume reuses
these. A handoff with a branch and commit offers **Open in a new session worktree**;
choose a registered folder whose repository contains that commit. The engine
refuses unsupported or unsafe repository settings before creating the worktree.

Cleanup refuses to discard uncommitted or unmerged work. **Also discard the
worktree and branch** explicitly authorizes that loss; ignored files are removed
with the worktree. Worktrees share the repository's git metadata. See the
[session CLI reference](/reference/cli/#command-olivares-session-start) for
worktree options and [session runtime API](/reference/session-runtime-api/) for
profile permissions, lifecycle calls and isolation limits.
