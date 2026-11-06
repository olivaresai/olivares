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

**API keys** on **Tools** (`/agent-tools`) holds API keys and local-model
endpoints. Add and test the provider there, then use it from New session. A provider key and a tool
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
profile and folder. For the README Compose installation, choose **Folder → Another folder…**
and enter `/project` to work on your mounted host project. Leaving the default
folder needs no typed path. Leave First message empty to start, then type in
the conversation, or enter it before pressing **Start**.

A tool using a provider key uses its saved model or lets you select a model
returned by the connection test. If the model is required but the test has not
returned any, test the connection under **API keys** on **Tools** first. Claude Code can use
its native default model.

For the README Compose installation, run the CLI inside the engine container.
It finds the local engine and its certificate there. Sign in with the Olivares
administrator account you created during setup, not your tool subscription.

When using a provider key with a tool that requires a model, such as Codex,
save a default under **API keys** on **Tools** before running `session start` below. If neither
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

Under **What the agent may do**, **Supervised** makes the tool ask before
acting. In **Rules**, the request waiting for you is open beside the queue with
its command, what it reaches and the rule that asked. Choose **Approve once**
(`A`), or **Deny** (`D`) and add a note. A launch that requires an organization
approval waits there too; send your message once the launch is approved.

From a terminal, `olivares session start`, `send` and `follow` show the same request
while a turn runs, with the command or path the tool will run. At a terminal they ask
`Allow? [y/N]` (Enter denies). Without one they print the request and the commands that
answer it, with its id filled in: `olivares session approve <session> <request>` or
`olivares session deny <session> <request> --note "why"`. Each answers exactly that
request and refuses one the session no longer waits on. The engine denies the tool call
if nobody answers before its deadline.

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

## Be told when a session needs you, with the tab closed

Under **Settings**, **Session notifications**, choose **Notify me**. While a tab of
the console is open in the background the browser tells you, as before. On a browser that
supports Web Push, the same switch also registers that browser with your engine, so a session
you started that asks for an approval, or ends, reaches it with the tab closed. Tap the
notification to open that session; its approval is waiting there. Turning the switch off
removes the registration. A person can register up to 10 browsers; the oldest leaves for an
eleventh.

To install the console, use the browser's own **Install** or **Add to Home Screen** action.
On Android, Chrome offers **Install app**. On an iPhone or iPad, notifications only work from
the installed app: tap **Share**, then **Add to Home Screen**, open Olivares from the Home
Screen and turn the switch on there. The engine must be reached over HTTPS, which browsers
require for installed apps and for push. With the engine’s built-in local certificate,
the console keeps notifications in the open tab and skips Web Push. Use a trusted TLS
certificate or a TLS-terminating reverse proxy for closed-tab notifications.

The engine sends each notification encrypted to your browser's own key. Google, Mozilla or
Apple run the push service that relays it, and that service sees an encrypted body and the
time it was sent, never the text. A notification carries the name you gave the session, or
none, and a fixed sentence in your language; it never carries what the session wrote. Your
engine needs to reach the push service on port 443; where it cannot, the switch still works
for open tabs and Settings says this device could not be registered.

## Optional profile and git settings

For an existing configured tool home, the **Provider profiles** tab of **Sessions**
(`/sessions?tab=profiles`) lets an administrator register a profile and inspect
launch readiness. A named account's profile and source bindings are also in its
detail on **Tools**.
The homes must exist on the execution host. Use **More options → Advanced launch
options** in New session to select that profile. Binary overrides choose an
executable; otherwise the engine tries the newest verified managed install,
then the tool on its `PATH`. See [Configuration](/reference/configuration/).

For a git repository's top folder with at least one commit, **Work in a new git
worktree** creates a branch and working directory for the session. Resume reuses
these. A handoff with a branch and commit offers **Open in a new session worktree**;
choose a registered folder whose repository contains that commit. The engine
refuses unsupported or unsafe repository settings before creating the worktree.

A folder can store one setup command for new worktrees. A person must approve
its exact text, by SHA-256, before a worktree start is accepted; changing one
character requires approval again. The approved command runs once, in the new
worktree, before the session's first turn; a failure stops the start and shows
its output. If setup fails, resume runs the command again; after successful setup or a
recorded `--no-setup`, resume does not. See
[Approving a folder's setup command](/reference/session-runtime-api/#approving-a-folders-setup-command).

Cleanup refuses to discard uncommitted or unmerged work. **Also discard the
worktree and branch** explicitly authorizes that loss; ignored files are removed
with the worktree. Worktrees share the repository's git metadata. See the
[session CLI reference](/reference/cli/#command-olivares-session-start) for
worktree options and [session runtime API](/reference/session-runtime-api/) for
profile permissions, lifecycle calls and isolation limits.

## Course AI-use presets

The built-in template list includes five **AIAS** course presets. These are
operational adaptations of the [AI Assessment Scale](https://researchonline.jcu.edu.au/87283/),
not a certification or a claim that a model can police academic honesty.

| Template | Session behavior |
| --- | --- |
| AIAS 1 — No AI | Refuses session launch and resume. |
| AIAS 2 — Plan only tutor | Makes the session folder read-only; instructs the tutor to give hints and questions without code or a final answer. |
| AIAS 3 — Scratch practice | Permits edits in a separately configured scratch workspace; attach the course folder read-only as described below. |
| AIAS 4 — AI with review | Permits edits, asks the learner to review and attribute AI contributions, and requires conversation recording. |
| AIAS 5 — Full AI | Permits full tool use within the session's filesystem boundary; run administration, governance approvals and conversation recording still apply. |

Select the template when starting a session. Template terms are applied by the
server on launch and resume. An explicitly narrower permission choice is kept;
selecting Full cannot override the tutor's plan mode. Department defaults and
assignment remain Business features, using the existing department template setting.
A default is not a ban on selecting another template or starting a session elsewhere.

For **Plan only tutor**, select the course folder. Native sessions use Linux
Landlock; container sessions use a read-only workspace mount. The preset requires
truncation protection: a native launch on a kernel older than Landlock ABI 3 is
refused rather than permitting existing files to be truncated.

For **Scratch practice**, register a separate, empty scratch folder as the
workspace root (`root_path`, `mount_mode: "rw"`). Attach the course folder through
that workspace's `read_only_folders`. Keep the two folders separate: the course
folder must not contain the writable scratch folder, and neither may overlap a
writable account home. Start the session in the scratch workspace using this
preset. The existing confinement grants writes to scratch and reads to the
course folder, while the tool's account state and private temporary directory
remain writable. Missing confinement or truncation protection refuses the launch.
The template does not create folders or change workspace registrations. It refuses
a launch or resume without a registered workspace and read-only reference folders.
The instructor must attach the actual course folder and grant the learner access
to the scratch workspace; attaching an unrelated folder does not protect the course.

Tutor guidance is a model instruction. The filesystem restriction prevents writes
to the course folder even if the model disobeys, but cannot prevent it from
printing solution text. Recording captures the conversation; it does not certify
attribution or assessment compliance. The No AI template disables launches that
use it, not other templates, external AI services, or already running sessions.
