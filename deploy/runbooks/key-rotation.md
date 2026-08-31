<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Runbook — audit signing-key rotation

**Severity:** planned maintenance, or **SEV1** on suspected key compromise.

> **Read this first — the honest state.** The on-box audit signing key **can** be rotated
> chain-continuously. Under CMEK custody, `olivares keys rotate` mints a **new sealed key** and carries the
> retired key forward as non-secret `prior_public_keys` history. Rotation is completed by
> `olivares audit key-transition`, which records an **off-box-signed epoch boundary** per tenant. The
> boundary FENCES the retired key to the last sequence it legitimately signed: after it, a retired key
> can no longer validate the tail (closes **F-07**). Per-event signatures remain **on-box Ed25519** (the
> hot path is never routed off-box); the boundary and checkpoints are what the off-box KMS signer seals.

## Keys involved
- Audit per-event key — the Ed25519 key that signs every event and, by default, the on-box checkpoints.
  - **CMEK custody (recommended):** a sealed envelope (`keys wrap`/`keys rotate`), opened at boot through the
    customer KEK (`OLIVARES_KEY_WRAP`); the key at rest only exists KEK-wrapped, and the envelope carries the
    `prior_public_keys` rotation history (`keys status` prints it).
  - **BYOK/minted:** a plaintext base64 key (`<data-dir>/audit-signing.key`, `0600`, fails closed on loose
    perms) or a mounted/env Secret. No envelope history — fence externally (see path C).
- Off-box checkpoint + boundary signer: `OLIVARES_LEDGER_SIGNER=aws-kms|gcp-kms|azure-kv` (+ provider vars,
  `cmd/olivares/ledgersigner.go`). **Required** to seal an in-chain rotation boundary (`audit key-transition`).
- `<data-dir>/catalog-signing.key` — a **separate** artifact-signing key; **not** the audit key.

## A) Supported path — chain-continuous rotation under CMEK (engine STOPPED)
Run every step with the engine **stopped** (no serving), so each tenant's tail is frozen while the boundary
is recorded.

1. **Mint the new sealed key**, preserving the rotation history:
   ```bash
   olivares keys rotate --in /var/lib/olivares/audit-signing.key.sealed \
                        --out /var/lib/olivares/audit-signing.next.sealed
   # prints the new public key + "prior generations kept: N"
   ```
2. **Swap the new envelope into place** (atomic): move `audit-signing.next.sealed` over the path the engine
   opens (`OLIVARES_AUDIT_SIGNING_KEY_WRAPPED_FILE`). Keep the superseded `--in` file off-box until step 5
   verifies, then shred it.
3. **Record the epoch boundary** — the step that actually fences the retired key. It appends one
   off-box-signed `audit.key.rotation` marker per tenant binding the retired key's fingerprint to that
   tenant's current tail (`prior_last_seq`):
   ```bash
   olivares audit key-transition --data-dir /var/lib/olivares    # all tenants + system chain
   # requires OLIVARES_LEDGER_SIGNER; prints prior/new fingerprints and each marker seq
   ```
   The retiring key is taken from the envelope's most recent prior generation (override with
   `--prior-pubkey <base64>` for BYOK).
4. **Restart serving.** From here the new key signs every event (from each tenant's tail+1); the retired key
   is fenced to `(0, prior_last_seq]`.
5. **Verify.** Advisory verification auto-derives the fences from the in-chain markers:
   ```bash
   olivares audit verify --tenant <id> --strict \
     --pubkey "<off-box-alg>:<off-box-pubkey>"      # attacker-resistant: pin the off-box signer
   # event_keys.fenced=true; key_rotation_markers.ok=true; a fresh checkpoint anchors the new tail
   olivares audit checkpoint                          # anchor the new key going forward
   ```

## A2) Migrating custody to another KEK — another cloud, account, region or vault (engine STOPPED)

Moving the KEK is **not** the same thing as rotating the signing key, and choosing wrongly costs an
auditor a re-pin they did not need:

| You want | Command | What moves |
|---|---|---|
| A new custodian, same signing key (the usual "we are changing cloud") | `keys rewrap` | Only the KEK. `public_key` and `prior_public_keys` are untouched, so **every pin an auditor already holds stays valid**. No fencing needed |
| A new custodian **and** the signing key retired (you are moving *because* of a compromise) | `keys rotate` | New signing key; the old public key becomes history. Section **A** applies in full, fencing step included |

Declare both identities, then run the ceremony once. The **source** is the identity that sealed the
envelope you have; the **destination** is what everything is sealed under from now on:

```bash
# DESTINATION — the identity the engine keeps using afterwards
export OLIVARES_KEY_WRAP=azure-kv
export OLIVARES_KEY_WRAP_AZURE_VAULT_URL=https://new-vault.vault.azure.net
export OLIVARES_KEY_WRAP_AZURE_KEY_NAME=olivares-kek
export OLIVARES_KEY_WRAP_AZURE_TOKEN_FILE=/run/secrets/azure-token

# SOURCE — same variable shape, one namespace down. Read ONLY by these ceremonies.
export OLIVARES_KEY_WRAP_OLD=aws-kms
export OLIVARES_KEY_WRAP_OLD_AWS_REGION=eu-west-1
export OLIVARES_KEY_WRAP_OLD_AWS_KEY_ID=alias/olivares-kek

olivares keys rewrap --in  /var/lib/olivares/audit-signing.key.sealed \
                     --out /var/lib/olivares/audit-signing.rewrapped.sealed
# prints: rewrapped <in> under azure-kv <new key id> -> <out>
```

Then **swap the new envelope into place** and keep the old one until the engine has booted against
the new custodian, exactly like step 1 of the rotation above.

> ⚠ **This step used to be written without `--out`, overwriting the envelope in place**, and that is
> the one shape of this ceremony that cannot be undone: if the destination KEK turns out not to be
> openable — an Azure vault whose key version was not pinned is the case to watch — the engine
> will not boot and **there is nothing to go back to**.
> Writing beside it costs one flag and one `mv`.
>
> Since 2026-08-21 the in-place form **asks first**, and refuses outright when there is no terminal
> to ask (a prompt written to a pipe is answered by EOF, which is not consent). If you deliberately
> want it in place from a script, say so with `--yes`.

Same-provider moves use the identical mechanism — another AWS account or region, another Key Vault,
another GCP key. When both sides are AWS and need **different principals**, give the source its own
credentials instead of the shared `AWS_*` ones:

```bash
export OLIVARES_KEY_WRAP_OLD_AWS_ACCESS_KEY_ID=...      # falls back to AWS_ACCESS_KEY_ID if unset
export OLIVARES_KEY_WRAP_OLD_AWS_SECRET_ACCESS_KEY=...
export OLIVARES_KEY_WRAP_OLD_AWS_ENDPOINT_URL_KMS=...
```

**Then close the window, in this order:**

1. **Read the printed line and confirm the destination**, then `olivares keys status` — it reports
   `kek` and `kek_migration_source`. A migration that landed in the wrong account of your own is a
   recoverable operator error, but only if you look.
2. **Unset `OLIVARES_KEY_WRAP_OLD*`.** While it is set, *every* ceremony opens with it, so an ordinary
   rewrap against the current KEK will be refused until you remove it. That refusal is deliberate — the
   alternative is silently trying a second KEK — and the error names the variable to unset. The engine
   itself never reads the namespace and warns at boot if it finds it.
3. **Remove the old cloud's credentials from the environment.** This ceremony is the only moment both
   clouds' credentials coexist in one process; it is the highest-density secret moment in the whole
   custody lifecycle, and it should last exactly as long as the command does.
4. **Shred the pre-migration envelope** once the engine boots against the new one.

An envelope whose custody metadata has been edited is **refused, not migrated**: the source envelope is
opened and authenticated before anything is carried forward. If a migration fails with
"custody metadata does not authenticate", treat it as a tampering incident, not a config problem.

## B) Off-box (KMS) checkpoint key rotation
Rotating the KMS *checkpoint* key is independent and transparent to per-event signatures (the checkpoint
verifier accepts any pinned candidate):
1. Rotate the key version / key in the KMS and export the new public key (`GetPublicKey`).
2. Verify with **both** old and new public keys pinned (`--pubkey` repeatable) so historical checkpoints
   still verify, and `audit checkpoint` so the new key anchors going forward.

## C) Attacker-resistant / BYOK verification — pin the boundary explicitly
When there is no off-box signer (so no in-chain boundary can be sealed), or an external auditor wants to
verify independently of the engine, pin every generation WITH its boundary. This is the load-bearing
control per docs/SECURITY-HARDENING.md — the external pin, not the advisory on-box check:
```bash
olivares audit verify --tenant <id> --strict \
  --pubkey  "<off-box-checkpoint-key>" \
  --event-pubkey "<RETIRED_KEY_b64>@<last_seq>" \   # retired: valid only up to last_seq
  --event-pubkey "<CURRENT_KEY_b64>"                # current: no upper bound
# @<lo>:<hi> pins an explicit window; a bare key is the current generation
```
The retired key's `last_seq` is the `prior_last_seq` of its `audit.key.rotation` marker (or, for BYOK
without markers, the boundary you recorded off-box at rotation time). Without a boundary the retired key is
trusted for **every** sequence — that is exactly F-07, so always pin the boundary.

## Defense in depth (why the boundary is trustworthy)
- **Off-box signer** (`OLIVARES_LEDGER_SIGNER`): the epoch boundary is signed by the KMS/HSM key, never an
  on-box key. This is deliberate — the boundary is the control that revokes a retired *on-box* key, so an
  attacker holding a retired on-box key + DB write cannot forge a boundary that re-widens it. The engine
  refuses an unsigned/on-box-only `key-transition`.
- **WORM archive export** (`olivares audit archive export` → immutable storage): the boundary marker rides
  the archive like any event, and with the off-box checkpoint key pinned (`audit archive verify --pubkey`)
  its signature and position re-verify offline. But the marker does NOT auto-fence the archive's per-event
  signatures: the offline verifier is single-pass and constant-memory, and the archive's `keys.json` lists
  the generations UNFENCED (and is itself unauthenticated — it rode with the archive). So the attacker-
  resistant fence over an archived rotated chain comes from the operator PINNING each generation with its
  boundary, exactly as in path C — identical to the live external pin:
  ```bash
  olivares audit archive verify --dir /mnt/worm/audit/<id> --strict \
    --pubkey  "<off-box-checkpoint-key>" \
    --event-pubkey "<RETIRED_KEY_b64>@<last_seq>" \   # retired: fenced to its epoch
    --event-pubkey "<CURRENT_KEY_b64>"                # current: no upper bound
  ```
  A bare `audit archive verify` (advisory keys.json, no pins) verifies structure and signatures but does NOT
  fence retired keys — treat it as advisory, never as the F-07-resistant proof.
- **Sealed envelope AAD**: the `prior_public_keys` history is authenticated into the envelope's AEAD, so a
  disk-write attacker cannot append a key of their own to widen the candidate set.

## Compromise response (suspected key leak) — SEV1
1. **Declare SEV1.** A leaked on-box key can forge per-event signatures for the CURRENT epoch. Treat the host
   as compromised; preserve evidence; rotate host/operator credentials.
2. Rotate immediately via path A (CMEK) so the leaked key becomes a fenced retired generation and stops
   validating the new epoch. Ensure an **off-box signer** is configured so the boundary is sealable.
3. Pin the off-box key and the fenced generations for verification (path C); export to WORM.
4. **Strategic fix:** keep custody off-box (BYOK/HYOK/CMEK) so a host compromise never holds a key that is
   trusted beyond its fenced epoch.

## Key custody hygiene (at provisioning — prevents the worst case)
- Back up the sealed envelope (or, for BYOK, `<data-dir>/audit-signing.key`) off-box immediately. Losing the
  key material without a backup makes the historical ledger cryptographically unverifiable (also gates
  failover/restore — see [failover.md](failover.md)).
- Keep perms `0600` (owner-only); looser perms make the key read **fail closed** at boot.
- Configure an off-box checkpoint signer before you need to rotate, so a rotation can seal its boundary.
