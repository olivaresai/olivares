<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->
# Trust model of stored secrets and the personal vault

**Status.** The sealed secret store described in sections 1, 2 and 4 ships today. In section 3, the
owner-only reveal (`POST /v1/console/secrets/reveal`), the item format, authenticator codes, the
password generator, the console page (Vault), the `olivares vault` commands and `olivares vault
import` and `export` are built and are not in a published release yet. Section 5 lists what the
appliance adds; the appliance is **planned**.

> **In one sentence.** Olivares seals every stored secret with a key held by the engine. It is
> **not** end-to-end encrypted to each user and it is **not** zero-knowledge: whoever holds the
> engine's key and its database can read the values.

## 1. What is sealed, and with what

- Each stored value (provider keys, MCP credentials, session environment secrets) is encrypted with
  AES-256-GCM before it reaches the database. The scope is bound to the ciphertext as additional
  authenticated data, so a value cannot be replayed under another scope.
- One engine-held key seals the store. It is either 32 base64-encoded bytes in
  `OLIVARES_SECRET_STORE_KEY`, or the file `secret-store.key` that the engine creates in its data
  directory on first start (mode 0600; a wider mode is refused). The variable wins when both exist.
- Other secrets have their own key and purpose, so one sealed kind never opens as another: TOTP
  seeds, SSO secrets and event-source secrets.
- Only the engine opens a value, to resolve a `store:<name>` reference for a connector or a
  session. Listing returns names, a hint, descriptions and times, never values. The hint is the
  first 12 hex characters of an unsalted SHA-256 of the value, so a low-entropy secret can be
  guessed from it. A personal vault item has no such hint: its hint is its kind (section 3).
- Console and API writes and deletes require the deployment's step-up policy; with the default
  policy that is the strength the administrator signed in with. The `olivares secrets` command
  needs a named actor and a reason. Every mutation is audited.
- The scope is bound to each ciphertext, but the secret's name is not: someone who can write to
  the database can move a sealed value between two names in the same scope.
- If no key can be loaded, writes and `store:` references fail closed. The engine never stores or
  accepts a cleartext value as a fallback.

## 2. Who can read a value

| Holder | Can read the values? |
| --- | --- |
| A copy of the database, a replica or a dump, **without** the key | No |
| The key **without** the database | No |
| Anyone with the database **and** the key (shell on the host, a backup that carries both, the operator of the engine) | **Yes** |
| The running engine | Yes, by design: it replays values to connectors and sessions |
| Another Olivares user | No route returns a stored value to them. Only the owner of a `vault/<user-id>/` item can reveal it (section 3). A tenant administrator can still give a secret to a session as environment, or point a connector at a `store:` reference, and what runs there can send the value elsewhere |

A key supplied through `OLIVARES_SECRET_STORE_KEY` lives in the process environment and in
whatever holds that variable for the deployment. The file-mode check applies only to the key file.

Sealing therefore protects against theft of the database alone. It does not protect against the
party that runs the engine. That is the same model as the provider keys the product stores today.

## 3. The personal vault

The vault reuses the same store, the same sealer and the same audit. A vault item is a secret
under a `vault/<user-id>/<item>` name in the organization's secrets (`?scope=tenant`). It adds one
deliberate exception to "no route returns a value": an owner-only **reveal**. Only the signed-in
person whose id is in the name can write, list or reveal the item. Another administrator, the
superadmin and every API or session token are refused. Reveal requires the deployment's step-up
policy, is rate limited per person, is audited without the value (`secret.reveal`, with the actor
and the entry id), and answers with `Cache-Control: no-store`. Global, `mcp/` and `env/` secrets
keep refusing reveal. `olivares vault get <item>` calls it.

Every member of an organization, whatever their role, keeps a vault: the organization's secret
scope admits any member, and a member reaches only the `vault/<their-id>/` names in it. The
`mcp/` and `env/` names stay with the organization's administrators. A vault holds at most 500
items and an item's description at most 1 KiB. The count is checked when an item is created, apart
from the write itself, so creates that arrive together can pass it together; the API's per-person
write limit bounds how far.

An item is a small JSON document: a **login** (username, password, website, optionally an
authenticator key), a **note** (text) or an **authenticator** item (the key). The engine checks
the document before it seals it, and a list shows an item's kind (`login`, `login+totp`, `note`,
`totp`) as its hint, without opening anything. The list never carries a value.

**Authenticator codes.** The authenticator key is a base32 secret or the `otpauth://` link of the
service's QR code. `POST /v1/console/secrets/totp` and `olivares vault totp <item>` return the
current RFC 6238 code and the seconds it has left. The key never leaves the engine: neither a
list nor a code answer carries it, and the code is not in the ledger. It is guarded like reveal:
the deployment's step-up, the owner only, the same per-person limit, and one ledger entry
(`secret.totp`) with the actor and the entry id. Reading the key itself back is a reveal.

**Password generator.** `POST /v1/console/secrets/generate` (the console's Generate button) and
`olivares vault generate` (which runs in the CLI, with no server) make a random password with
`crypto/rand`: 8 to 128 characters, at least one of every kind you choose. Nothing is stored or
recorded.

**Copying.** The console copies a password or a code to the clipboard and takes it back 30 seconds
later, only if the clipboard still holds what it put there. A browser that does not let a page read
the clipboard gets a blind clear on the timer; if that clear is refused (the tab is in the
background) it is retried once when the window returns and once when the page is left, and those
retries clear only what they can read and see unchanged.

**Import and export.** `olivares vault import <file> --from bitwarden|keepass|1password|chrome|proton`
reads the CSV that program exports, and `--from olivares` reads an export of this vault. Each row
becomes one item through the same put as `olivares vault put`: the same checks, the same 500-item
limit (an import that would pass it is refused before anything is written), the same ledger entry
for each write. An item that is already there is kept unless `--replace` is given. Rows the vault
has no item for (cards, identities, aliases) and data an item has no field for (folders, tags,
custom fields) are counted in the result and not stored. Nothing prints or logs a value;
`--dry-run` checks the file and prints counts only. If the engine fails half way, every item
stored is complete and running the same import again stores the rest. `olivares vault export
--file <path>` reveals every item, one `secret.reveal` ledger entry each, at the reveal limit of
about 20 items a minute, and writes one JSON file. **That file, like the CSV another program
exports, holds every secret in the clear.** The export creates it for the owner alone (mode
0600), never over an existing file, and leaves none behind if it fails. Keeping it private and
deleting it afterwards is the owner's part; the engine cannot do that.

The trust model of the vault, stated plainly:

- Vault items are sealed by the **engine's key**, like every other stored value. They are **not**
  encrypted to the user's own key.
- Whoever holds the engine key and the database can read every vault item, including a TOTP seed
  (the engine computes the codes, so it holds the seed).
- There is no user-held key, so a forgotten sign-in does not make items unreadable, and an operator
  cannot be locked out of them.
- A zero-knowledge mode with a user-held key is a possible later option. It is not part of the
  planned vault, and nothing in the product or its documentation claims it.

Business shares an item by adding access grants to the one stored row. The ciphertext is not
copied per recipient and no per-recipient re-encryption happens, because the engine does the
sealing.

## 4. Where the key lives

**Backup and restore.** `olivares dr backup` carries the key file in the encrypted bundle (sealed
under the operator's key-encryption key, a passphrase or a raw key), so a restore reopens what
was sealed. A key supplied through `OLIVARES_SECRET_STORE_KEY` is **not** put in the bundle: set
the same value on the restored host. Without the key, sealed values do not open. For a bundle
that carries key probes, `dr restore` reports `key custody intact` only when every key in effect
matches the source's. See
[DR-RUNBOOK.md](DR-RUNBOOK.md).

Treat a backup bundle together with its key-encryption key as equivalent to the secrets
themselves, and keep the two apart.

**Customer-managed keys (CMEK, Business).** Today CMEK seals the audit, catalog and policy signing
keys and the operator's configuration files in envelopes that only the customer's key service
opens. **It does not wrap the key of the secret store.** Moving the store key into the customer's
key service is not offered yet, and this page will describe it when it is. Until then, an
operator who needs the store key outside the data directory can supply it through
`OLIVARES_SECRET_STORE_KEY` from their own secret manager. See the CMEK row of
[editions.md](editions.md).

## 5. What the appliance adds

The Olivares appliance (Business) runs the same engine, so the sealer, the key and every statement
above are unchanged. The appliance does not add end-to-end encryption or a different key model.
It is planned: no appliance image has shipped. What it adds is hosting:

- **A TLS origin.** The engine console is served over HTTPS (port 8443) with a certificate created
  on the machine at first boot, so values travel to the browser over TLS. A browser extension
  needs an origin whose certificate it trusts; a publicly trusted certificate (ACME) is not wired
  into the appliance yet.
- **Backup.** The same `olivares dr backup` and its key custody apply. The appliance adds no
  separate backup of the key.

## What this page does not claim

Olivares does not claim zero-knowledge or end-to-end encryption for stored secrets, the personal
vault or the appliance. A claim of that kind anywhere in the product's text is a defect: report it.
