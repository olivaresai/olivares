<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Appliance image build toolchain

This is the single builder for the Debian 13 amd64 appliance recipe. The guest remains
Debian 13. `input-lock.json` binds an immutable Debian base, one signed Debian snapshot,
exact OS root versions, the full snapshot package index and the Python artifact closure.
Transitive OS candidates come only from that index or the immutable base; no live archive
or second repository is enabled. Apt verifies signatures and artifact hashes. Bootstrap
also checks the exact signed index and decoded package-index hashes before installation.

KIWI 11.0.4 is built from its hash-verified official source archive into a private venv.
All runtime wheels and the Poetry backend have exact hashes. The source build and install
run in Docker BuildKit `RUN --network=none` stages. No global pip, implicit dependency
resolution, unlisted source build or mutable Dockerfile frontend is used. The bundled
frontend belongs to the measured Docker engine. Its support is a hosted prerequisite.
The former OBS package route is incompatible with the selected Debian snapshot: its
Typer dependency requires >=0.19.0, while the snapshot supplies 0.15.2-1.

The source closure is not runtime qualification. The builder must pass version, CLI/help,
imports, dependency consistency, required tools, boot data and KIWI schema checks. The
resulting image carries inventories and the source/derived-wheel provenance. The runner
retains them in its receipt with the produced immutable image ID. Every ISO/qcow2/OVA build,
boot and import remains a separate recipe obligation.

The recipe's consumed Interface is part of this lock (Root amendment, 2026-09-22):
`required_programs` names every program the recipe itself runs in the builder, including
`dpkg-scanpackages`, whose root `dpkg-dev` is pinned from the same hashed snapshot index.
`toolchain.dracut_modules_dir` is where `install.py` copies the guest dracut modules from the
verified KIWI source; `required_files` qualifies the installer's modules there. The recipe
reads that path from the lock, never the installer's temporary source tree.

## Prerequisite and states

| State | Meaning | Exit |
| --- | --- | --- |
| `eligible` | The observed prerequisites passed for this attempt | 0 |
| `unavailable` | A measured environmental prerequisite failed | 2 |
| `defect` | Inputs, evidence or resource custody are invalid or incomplete | 1 |

Missing evidence is never success. Booleans must be booleans; capacity values must have
valid numeric types. A QMP greeting and successful capability negotiation must precede an
explicit `prelaunch` state with `running: false`. This proves accelerator initialization,
not guest boot. Failed KVM and selected TCG attempts both remain in the receipt.

The 15,000,000,000-byte build budget is declared, not measured. Published runner storage
figures are decimal GB and are not free-space measurements. Actual bytes are sampled after
toolchain allocation and again at recipe admission. Peak build disk and boot duration remain
unknown until measured. TCG initialization establishes no useful full-boot time budget.

Run only on a disposable hosted runner:

```sh
bash appliance/test/runner/preflight.sh \
  --lock appliance/images/toolchain/input-lock.json \
  --evidence "$RUNNER_TEMP/appliance-a2-preflight"
```

The Bash entry point calls `collector.py`. The default standalone run removes its builder
image after the probe. This receipt can be audited, but cannot admit a nonexistent image:

```sh
python3 appliance/test/runner/judge.py \
  --lock appliance/images/toolchain/input-lock.json \
  --receipt "$RUNNER_TEMP/appliance-a2-preflight/receipt.json"
```

This command is a historical re-judgement, not permission to consume a builder. It makes no
mutable lookups. Admission requires the current expected context and live identity checks.

## Recipe integration within one job

The recipe must consume this builder; it must not install another KIWI version. Its owning
job performs these acts in order, using its own attempt directory:

1. Run preflight with `--retain-builder`. It releases live containers and loop resources,
   and explicitly retains the passive image for this same run, job and attempt.
2. Read the observed `attempt.id` and `observations.container_toolchain.image_id` as proposed
   identities. Select the accelerator the recipe will use and pass it independently with
   `admit.py --attempt ID --image-id sha256:... --accelerator kvm` (or `tcg`), with the
   same `--lock` and `--receipt` paths. This choice must match the terminal proved trial;
   the summary cannot replace the bounded, ordered history. Fallback requires confirmed
   cleanup and no prior successful trial. Invalid or untyped proof refuses before live
   lookups. The adapter independently checks current run/job,
   source commit, input digest, architecture, accelerator, Docker identity/owner label,
   absence of probe containers and fresh free bytes. A copied historical receipt refuses.
3. Consume `image_id` and `accelerator` from the successful admission. Invoke the recipe only
   on that exact image and accelerator, never on a mutable tag. Keep its existing
   capability/device contract. This check grants no host or product authority.
4. In a `finally`/`always` cleanup step, call the same `admit.py` arguments plus `--release`,
   even if the recipe fails or is cancelled. Preserve `builder-release.json` and its result.
   A failed or interrupted release remains unconfirmed; do not claim cleanup or delete
   evidence. Cross-job reuse is refused. No registry publication is part of this handoff.

Example adapter invocation (the two shell variables contain observed public IDs only;
`kvm` is the consumer's explicit intended accelerator, not a value inferred by the adapter):

```sh
python3 appliance/test/runner/admit.py \
  --lock appliance/images/toolchain/input-lock.json \
  --receipt "$RUNNER_TEMP/appliance-a2-preflight/receipt.json" \
  --attempt "$APPLIANCE_ATTEMPT" --image-id "$APPLIANCE_IMAGE_ID" --accelerator kvm
```

`--release` checks the same job/attempt and immutable ownership before removal. It does not
require `--accelerator`, a still-valid proof, or a still-clean recipe worktree. A failed build
does not strand cleanup behind a source check. It records observed image removal separately from live-child cleanup.

## Cleanup and recovery

Every command has a deadline and a bounded diagnostic tail. Container IDs are recorded before
start; only exactly owned IDs can be stopped and removed. A loop device must still name the
owned backing file before format and detach. Backing files remain until unmount, detach and
absence have all been observed. Failure never causes recursive deletion of the work directory.
A Docker CLI timeout is not evidence that its daemon build ended: the result stays defective,
with cleanup unconfirmed, for disposal or inspection of that owning disposable runner.

The workflow invokes `workflow.py` under Actions' real Bash errexit settings. The wrapper
finalizes the receipt, exit classification, bounded log and hashes on refusal or interruption.
If the collector leaves no receipt, it writes an explicit incomplete receipt; it never calls
that success. Hard runner loss can prevent finalization/upload and remains missing evidence.
The harness and real preflight are **two jobs on separate hosted VMs**. No receipt transfers
eligibility from the harness VM to the preflight or from preflight to another recipe VM.

## Evidence and source checks

`receipt.json` retains the attempt UTC bounds/elapsed time; run/job identity; source and
build-input digests; runner UID/home path, image/kernel/mount/tool facts; actual capacity;
container qualification/inventory and immutable ID; every accelerator trial; loop ownership;
cleanup and retained-image custody; refusals and classified judgement. It includes no secret
environment. `admission.json` records current checks; `builder-release.json` records release.

```sh
python3 -m unittest discover -s appliance/test/runner -p 'test_*.py' -v
```

These are source controls using pure adapters and small owned Python processes, not container,
privilege or guest tests. Mutants cover omitted dependencies, artifact hashes, moved snapshot,
untyped evidence, stale admission, failed cleanup, QMP protocol and workflow exit handling.
The hosted owner must still execute the exact composed head/lock, retain all exits and runtime
inventories, test timeout/failure cleanup there, and qualify the recipe formats individually.
