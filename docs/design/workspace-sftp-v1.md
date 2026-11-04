# SFTP and SCP access to a workspace folder (v1)

Status: decided. Measured on the development source at `87c16c3e` (2026-11-04).
Edition: Community.

## Decision

The engine accepts SSH connections for the folders it already governs and serves
the SFTP protocol with the operating system's own `sftp-server`, started as a child
confined to the folder by the same Landlock policy a terminal or a session in that
folder gets. `scp` needs nothing more: OpenSSH 9 and later copies over SFTP.

Neither of the two routes the question named is taken as written:

- **(a) The OS OpenSSH serves the folder.** Rejected, because it does not hold on the
  measured facts below.
- **(b) The engine embeds an SFTP subsystem.** Taken in a narrower form: the engine
  embeds the SSH transport (`golang.org/x/crypto/ssh`, already a dependency of
  `core`), not the SFTP protocol. The protocol stays in the OS binary, so no SFTP
  library is added and nothing re-implements a tool.

The listener is off by default. The Connect card appears only when the engine
reports that the route exists on this host.

## What was measured

| Fact | Where |
| --- | --- |
| No code serves SFTP or SCP: no match in `core`, `modules` or `cmd`. `golang.org/x/crypto/ssh` is not imported anywhere yet. | `git grep -il sftp -- '*.go'` |
| The file API takes four trustee rights per folder (read, write, create, erase) through one door, `CheckFolderRight`, which asks the owner rule of a personal folder and then the policy engine. | `modules/sessions/workspace_api.go:64-120`, `personal_folder.go:52` |
| The file API's own jail resolves paths in user space and refuses `..`, absolute paths and symlinks that leave the folder. | `modules/sessions/workspace_fs.go:45-95` |
| A terminal already runs a confined child in a folder: same rights, same Landlock policy as a session (protected engine data, read-only folders, write-without-delete), a hub that closes it when the folder is removed, and audit entries in the signed ledger. | `modules/sessions/terminal.go:198-285`, `terminal_api.go:31-80` |
| The engine's OS account is a `nologin` system account that owns every folder it creates. | `packaging/nfpm/postinstall.sh:329-331` |
| `sshd` runs a forced command or a subsystem through the account's shell. Only `internal-sftp` skips the shell, and it has no folder jail without a root-owned `ChrootDirectory`. | OpenSSH `sshd_config(5)`, `ForceCommand`, `ChrootDirectory` |
| The appliance image installs `openssh-server` in both profiles but enables `sshd` only in the desktop profile; the container image has only `openssh-client`. | the appliance image recipe, `Dockerfile:75` |
| A bare `sftp-server` reads any file its user can (measured: `get /etc/hostname` succeeded), so the folder jail has to come from the OS, not from the protocol. | measured 2026-11-04 with OpenSSH 9.2p1 |
| `sftp-server` links only libc, so a confined child needs no more than the system directories every session child already gets. | `ldd sftp-server` |

## Why not (a)

1. The account that owns the folders cannot log in through `sshd` with its shell.
   Giving it a working shell, or adding a second account with access to every
   folder, widens the host to make one feature work, on every distribution.
2. `sshd` cannot ask the engine whether a grant still holds. A credential would hold
   until it expires, however the grant changed.
3. It exists only where `sshd` is installed and enabled: the desktop appliance,
   nothing else shipped.

## Authorization

- The Connect action is `POST /v1/m/sessions/workspaces/{ref}/sftp`. It needs the
  folder's read right, the same door as listing files. The credential is
  read-only unless the person also holds write, create and erase on the folder, the
  same three a terminal needs; a read-only folder, or one mounted write-without-delete,
  stays that way for SFTP.
- A person-less credential (an API token) is refused for the same reason a terminal
  refuses it: the deployment's step-up cannot be satisfied.
- The credential is an ephemeral Ed25519 key pair made by the engine. The private key
  is returned once and never stored; the engine keeps the public key, the folder, the
  mode and an expiry of ten minutes to connect. The user name is fixed (`olivares`):
  the key names the folder.
- The engine rechecks the person's rights on the folder every 30 seconds for every
  open SFTP session and ends the session when a right is gone or the folder is
  removed. A new connection with a credential whose grant was revoked is refused.

## Jail

The child is the OS `sftp-server` under the folder's Landlock policy, so a path
outside the folder, a `..` and a symlink that leaves the folder are refused by the
kernel, not by a path check in the protocol. Measured through a real `sftp` client:
`get` of a file beside the folder, of one in another directory, through `..` and
through a symlink out of the folder, and `put` outside the folder, all answer
`Permission denied`.

The ceiling, measured the same way: as for a terminal, the system directories stay
readable (`get /etc/hostname` and `ls /usr` succeed), so a person limited to a folder
can read the world-readable system files a terminal could. The engine's data
directory, its configuration and everything else outside the folder are refused.

## Audit

Each Connect, and each SFTP session, writes an entry to the signed audit ledger
(`sessions.sftp.issue`, `sessions.sftp.start`, `sessions.sftp.end`) with the person,
the folder, the mode and, at the end, the reason, the seconds and the bytes each way.
File names and contents are not recorded. A credential is not issued, and a session
is not started, when the ledger cannot record it.

## What the card shows

The workspace page asks `GET /v1/m/sessions/workspaces/{ref}/sftp`. It answers 404
when the listener is off or the host cannot confine a child, and the card is not
rendered. Otherwise the card offers Connect and then shows the exact `sftp` and `scp`
commands, the host key fingerprint to compare, the expiry, and the private key to save.

## Ceilings and upgrade triggers

- ponytail: credentials live in the engine's memory, so a restart ends them and a
  second node cannot honor the first node's credential; upgrade when two engines
  serve one folder at once.
- ponytail: a person who signs out of the console keeps an open SFTP session until the
  next rights check finds a right gone, the session idles out (15 minutes) or the
  8-hour cap; upgrade when sessions of the console are revocable by a port.
- Directory-issued or CA-signed credentials belong to Business. This design leaves one
  place, `issue`, where a signed certificate can replace the ephemeral key.

## Effort

Measured on the change that implements this record.

| | (a) OS OpenSSH | taken: SSH in the engine, protocol in the OS |
| --- | --- | --- |
| New Go code | a forced-command helper, a credential store the helper can read, an engine call the helper can make | about 860 lines in `modules/sessions` (service 665, routes 94, child launch 87 on Linux, 15 elsewhere) |
| New dependency | none | none: `golang.org/x/crypto` was already required by `core`, now also named directly in `modules/go.mod` |
| Host changes | `sshd` drop-in, a login shell for the engine account or a second account with access to every folder, per distribution | none; the OS `sftp-server` binary must exist |
| New configuration | `sshd_config` on every host | one setting, `OLIVARES_SESSIONS_SFTP_LISTEN`, off when unset |
| Proof here | needs a root `sshd` (the lab VM) | a real `sftp` client against the real listener and the real `sftp-server`, in one Go test |

The hand-rolled SFTP server of route (b) as the question named it would add a
protocol implementation or a new library; it is not measured because the OS binary
makes it unnecessary.
