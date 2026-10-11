---
title: "Your first hour with Olivares AI"
description: >-
  Install Olivares AI, sign in with an AI tool or connect a provider, start a
  conversation in your project folder, approve an action, stop and resume.
---

You need Docker Compose, a project folder you want the tool to work on, and
either a tool subscription, a provider API key or a local model.

## 1. Install and open the console

Follow the [README installation](https://github.com/olivaresai/olivares#install).
It selects the release image explicitly, mounts your host project at `/project`,
and prints a console address and a replacement one-time setup token. Use the
AppArmor instructions there when they apply to your Docker host.

Open the printed address. The first installation uses a self-signed certificate;
check that you are opening your own server before accepting the browser warning.
Enter the setup token, your email and a password to create the first
administrator. Keep the token and password private. The console signs you in
and opens the setup wizard.

The container runs tools on the server. A tool signed in on your laptop is a
different installation; its login is not copied into the container.

## 2. Choose a tool and its login

Use the wizard or open **AI tools** (`/agent-tools`). Each tool shows whether it
is installed and signed in. If it is missing, review and approve its installation.
Use the tool's sign-in control and complete its own login flow. For Claude Code
or Codex, approve the sign-in with your Claude or ChatGPT account.

On a native installation, the default instance uses the login of the user
running the engine. Logins made through Olivares remain separate instances.
The tools report the account, plan, models and usage windows they expose.
An unavailable value is not a zero balance; a failed refresh shows the last
snapshot as stale with the command that failed.

To use an API key instead, open **Providers** (`/providers`), add the provider,
and test the connection. For a local model, add an Ollama endpoint reachable
from the engine; no API key is needed. See
[Add a provider](/how-to/add-a-provider/). For a tool that requires a model,
such as Codex, select one returned by the connection test if the provider has
no saved default. Claude Code can use its native default model.

## 3. Start a conversation in your project

1. Open **Sessions** (`/sessions`) and choose **New session**.
2. Choose a ready tool. The form shows a folder and fills it for you. Leave it
   alone for a session in its own folder, or choose **Change folder** and enter
   `/project` to use the host folder mounted by the README installation.
3. Leave **First message** empty and press **Start**. You can type your message
   in the conversation after it opens. A profile and home paths are filled by
   the engine; you do not need to register them for this first session.

The container user, UID 65532, needs write access to the host project. The
[project-folder instructions](https://github.com/olivaresai/olivares/blob/main/deploy/compose/README.md#work-on-a-host-project-folder)
explain permissions. A session can change the mounted folder's contents; use a
project directory, not your whole home directory.

Ask the tool to explain the project. Read its reply in the conversation.
If **Start** cannot proceed, the form names what is missing. Follow that
instruction in **AI tools** or **Providers**, then return to the session.

## 4. Approve an action

To try an approval, choose **More options → Ask before each action** when
starting the session. Ask the tool to run a harmless command in the project.
When it requests permission, open **Approvals**, inspect the requested action,
and choose **Approve** or **Deny**. Confirm your decision and return to the
conversation to read the result. An organization policy can also require
approval before a session launches; an approver must accept that request first.

## 5. Stop and resume

Choose **Stop** on the session and confirm if asked. The tool process ends;
the conversation remains. Choose **Resume** on that same session and send
another message. Resume keeps the selected profile, account and folder and
checks that they can still be used.

For ongoing operation, the CLI and optional profile settings, see
[Operate a provider session](/how-to/operate-provider-sessions/).

## Native startup messages

Other installation methods have separate qualification limits; see
[Installation](https://github.com/olivaresai/olivares/blob/main/INSTALL.md#installation-qualification).
A native quickstart prints the console address and the next step for its setup
state. These are the fresh, returning and pending-setup messages:

```text
Next: Open the console; it guides setup, sign-in and your first session.
Next: Open the console and sign in to continue your work.
Next: Open the console to finish setup with the one-time token issued earlier.
```

When no host-wide inference credential is configured, the engine startup log
includes:

```text
session runtime: no inference credential source configured; stream-json launches are deny-closed
```

This limits Claude profiles using managed injection without a bound provider.
A bound provider key or the tool's authorized login is a separate credential
source; see [Operate a provider session](/how-to/operate-provider-sessions/#choose-how-the-tool-connects).
