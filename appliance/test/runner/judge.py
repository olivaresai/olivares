#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# The judgement half of the appliance image toolchain prerequisite check. It reads exactly
# two files, the checked-in input lock and the receipt a collector wrote, and classifies the
# pair. It reads no environment variable, no network and no live host fact, so the same lock
# and receipt always produce the same verdict and a receipt can be re-judged after the run
# that produced it has gone.
#
#   eligible    the measured prerequisites for the next attempt only.  exit 0
#   unavailable a named environmental condition is not met.            exit 2
#   defect      the lock, the contract or the evidence disagree.       exit 1
#
# Absent evidence is never eligibility. A mandatory observation that is missing, or recorded
# as not performed, is a defect: it means the collector skipped a step instead of measuring
# it. An observation that ran and honestly failed for an environmental reason is
# unavailable. A defect outranks an environmental shortage, because a receipt that
# contradicts itself says nothing reliable about the environment.
#
# eligible does not state that an appliance image builds, boots, installs or imports. Those
# need their own receipts from the recipe owner.

import argparse
import datetime
import hashlib
import json
import re
import sys

LOCK_SCHEMA = "olivares-appliance-image-toolchain-lock/v1"
RECEIPT_SCHEMA = "olivares-appliance-runner-receipt/v1"

EXIT_BY_STATE = {"eligible": 0, "defect": 1, "unavailable": 2}

PREFIXED_DIGEST = re.compile(r"^sha256:[0-9a-f]{64}$")
BARE_DIGEST = re.compile(r"^[0-9a-f]{64}$")
EXACT_VERSION = re.compile(r"^\d+\.\d+\.\d+$")
TIMESTAMP = "%Y-%m-%dT%H:%M:%SZ"

MANDATORY_LOCK_FIELDS = (
    "schema",
    "id",
    "recorded_at",
    "architecture.deb",
    "architecture.uname_machine",
    "base_image.reference",
    "base_image.index_digest",
    "base_image.manifest_digest_amd64",
    "distribution.codename",
    "distribution.version",
    "distribution.snapshot_basis",
    "toolchain.kiwi.upstream_version",
    "toolchain.kiwi.package",
    "toolchain.kiwi.package_version",
    "toolchain.kiwi.sha256",
    "toolchain.boxed_plugin.used",
    "invocation.container_runtime",
    "invocation.image_tag",
    "invocation.version_command",
    "invocation.required_capabilities",
    "budget.declared_build_bytes",
    "budget.declared_build_minutes",
    "budget.measured",
    "budget.basis",
    "probe.loop_image_bytes",
    "probe.accelerator_preference",
    "probe.required_qmp_status",
    "probe.qemu_timeout_seconds",
    "probe.container_timeout_seconds",
)

# The Fedora 44 builder's lock (toolchain/fedora44) names its base by distribution id: the Debian-only fields above are
# replaced by the Fedora architecture, the pinned repository metadata and the Fedora 44 key.
DEBIAN_ONLY_LOCK_FIELDS = ("architecture.deb", "distribution.codename", "distribution.snapshot_basis")
FEDORA_LOCK_FIELDS = tuple(f for f in MANDATORY_LOCK_FIELDS if f not in DEBIAN_ONLY_LOCK_FIELDS) + (
    "architecture.rpm", "distribution.id", "distribution.repositories", "distribution.key.fingerprint")


def is_fedora_lock(lock):
    return isinstance(lock.get("distribution"), dict) and lock["distribution"].get("id") == "fedora"


MANDATORY_RECEIPT_FIELDS = (
    "schema",
    "attempt",
    "attempt.id",
    "attempt.lock_id",
    "attempt.lock_digest",
    "attempt.started_at",
    "attempt.finished_at",
    "attempt.elapsed_ms",
    "runner.image_os",
    "runner.image_version",
    "runner.os_pretty_name",
    "runner.kernel",
    "runner.uname_machine",
    "runner.uid",
    "runner.user",
    "runner.home",
    "runner.passwordless_sudo",
    "runner.cgroup_fs",
    "capacity.processors",
    "capacity.memory_total_bytes",
    "capacity.memory_available_bytes",
    "capacity.evidence_avail_bytes",
    "capacity.probe_allocation_bytes",
    "capacity.declared_build_bytes",
    "capacity.declared_build_measured",
    "mounts",
    "tools",
    "observations",
    "refusals",
)

MANDATORY_OBSERVATIONS = ("container_toolchain", "accelerator", "loop_privilege")

LIMIT_NOTE = (
    "eligible states the prerequisites measured for the next attempt only: it is not a "
    "statement that an appliance image builds, boots, installs or imports."
)
TCG_NOTE = (
    "The accelerator selected was tcg. Initializing tcg establishes no useful full-boot "
    "time budget; a boot budget must be measured separately under the accelerator actually "
    "used."
)


class Verdict:
    """A classification with the reasons that produced it."""

    def __init__(self, state, reasons, notes):
        self.state = state
        self.reasons = reasons
        self.notes = notes

    @property
    def exit_code(self):
        return EXIT_BY_STATE[self.state]

    def payload(self):
        return {"state": self.state, "reasons": self.reasons, "notes": self.notes}


def walk(obj, pointer):
    """Return (found, value) for a dotted *pointer* inside nested dictionaries."""
    current = obj
    for segment in pointer.split("."):
        if not isinstance(current, dict) or segment not in current:
            return False, None
        current = current[segment]
    return True, current


def first_missing_segment(obj, pointer):
    """Return the shortest prefix of *pointer* that is absent, or None."""
    current = obj
    walked = []
    for segment in pointer.split("."):
        walked.append(segment)
        if not isinstance(current, dict) or segment not in current:
            return ".".join(walked)
        current = current[segment]
    return None


def delete_pointer(obj, pointer):
    """Delete a dotted *pointer*. Used by the tests to state one mutation at a time."""
    segments = pointer.split(".")
    current = obj
    for segment in segments[:-1]:
        current = current[segment]
    del current[segments[-1]]


def sha256_file(path):
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        for block in iter(lambda: handle.read(65536), b""):
            digest.update(block)
    return digest.hexdigest()


def validate_lock(lock):
    """Return the defect reasons of a lock considered on its own."""
    reasons = []
    if not isinstance(lock, dict):
        return ["lock.type"]
    if lock.get("schema") != LOCK_SCHEMA:
        reasons.append("lock.schema")
    fedora = is_fedora_lock(lock)
    for pointer in (FEDORA_LOCK_FIELDS if fedora else MANDATORY_LOCK_FIELDS):
        missing = first_missing_segment(lock, pointer)
        if missing is not None:
            reasons.append(f"lock.missing:{missing}")
    if fedora:
        repositories = walk(lock, "distribution.repositories")[1]
        if not (isinstance(repositories, dict) and repositories and all(
                isinstance(r, dict) and BARE_DIGEST.match(str(r.get("repomd_sha256", ""))) for r in repositories.values())):
            reasons.append("lock.repositories_unpinned")
        if not re.fullmatch(r"[0-9A-F]{40}", str(walk(lock, "distribution.key.fingerprint")[1])):
            reasons.append("lock.key_fingerprint")

    for pointer, pattern in (
        ("base_image.index_digest", PREFIXED_DIGEST),
        ("base_image.manifest_digest_amd64", PREFIXED_DIGEST),
        ("toolchain.kiwi.sha256", BARE_DIGEST),
    ):
        found, value = walk(lock, pointer)
        if found and not (isinstance(value, str) and pattern.match(value)):
            reasons.append(f"lock.digest_format:{pointer}")

    found, version = walk(lock, "toolchain.kiwi.upstream_version")
    if found and not (isinstance(version, str) and EXACT_VERSION.match(version)):
        reasons.append("lock.version_not_exact")

    for pointer in ("budget.declared_build_bytes", "budget.declared_build_minutes"):
        found, value = walk(lock, pointer)
        if found and not (isinstance(value, int) and not isinstance(value, bool) and value > 0):
            reasons.append("lock.budget_not_positive")

    found, measured = walk(lock, "budget.measured")
    if found and measured:
        reasons.append("lock.budget_labeled_measured")

    for pointer, ceiling in (("probe.qemu_timeout_seconds", 60),
                             ("probe.container_timeout_seconds", 1800),
                             ("probe.loop_image_bytes", 67108864)):
        found, value = walk(lock, pointer)
        if not (found and type(value) is int and 0 < value <= ceiling):
            reasons.append(f"lock.limit:{pointer}")
    if lock.get("architecture") != ({"rpm": "x86_64", "uname_machine": "x86_64"} if fedora else
                                    {"deb": "amd64", "uname_machine": "x86_64"}):
        reasons.append("lock.architecture_unsupported")
    for pointer, expected in (("invocation.container_runtime", "docker"),
                              ("invocation.version_command", ["kiwi-ng", "--version"]),
                              ("probe.required_qmp_status", "prelaunch")):
        if walk(lock, pointer)[1] != expected:
            reasons.append(f"lock.invocation_unsupported:{pointer}")
    preference = walk(lock, "probe.accelerator_preference")[1]
    if preference not in (["kvm", "tcg"], ["tcg"], ["kvm"]):
        reasons.append("lock.accelerator_unsupported")
    if walk(lock, "budget.measured")[1] is not False:
        reasons.append("lock.budget_labeled_measured")
    return reasons


def validate_receipt(receipt):
    """Return the defect reasons of a receipt considered on its own."""
    reasons = []
    if not isinstance(receipt, dict):
        return ["receipt.type"]
    if receipt.get("schema") != RECEIPT_SCHEMA:
        reasons.append("receipt.schema")
    for pointer in MANDATORY_RECEIPT_FIELDS:
        missing = first_missing_segment(receipt, pointer)
        if missing is not None:
            reasons.append(f"receipt.missing:{missing}")
    for pointer in ("capacity.processors", "capacity.memory_total_bytes",
                    "capacity.memory_available_bytes", "capacity.evidence_avail_bytes",
                    "capacity.probe_allocation_bytes", "capacity.declared_build_bytes"):
        value = walk(receipt, pointer)[1]
        if type(value) is not int or value < 0:
            reasons.append(f"receipt.type:{pointer}")
    for pointer in ("runner.image_os", "runner.image_version", "attempt.id"):
        value = walk(receipt, pointer)[1]
        if not isinstance(value, str) or not value.strip() or value.lower() in ("unknown", "unavailable"):
            reasons.append(f"receipt.type:{pointer}")
    for pointer in ("runner.passwordless_sudo", "capacity.declared_build_measured"):
        if type(walk(receipt, pointer)[1]) is not bool:
            reasons.append(f"receipt.type:{pointer}")
    for pointer in ("capacity.processors", "capacity.memory_total_bytes"):
        value = walk(receipt, pointer)[1]
        if type(value) is int and value <= 0:
            reasons.append(f"receipt.range:{pointer}")
    uid = walk(receipt, "runner.uid")[1]
    if type(uid) is not int or uid < 0:
        reasons.append("receipt.type:runner.uid")
    if not isinstance(receipt.get("mounts"), list) or not receipt.get("mounts"):
        reasons.append("receipt.type:mounts")
    if not isinstance(receipt.get("tools"), dict) or not receipt.get("tools"):
        reasons.append("receipt.type:tools")
    if not isinstance(receipt.get("refusals"), list):
        reasons.append("receipt.type:refusals")
    return reasons


def check_binding(lock, receipt, lock_digest):
    """Return the reasons the receipt is not bound to this lock and this attempt."""
    reasons = []
    attempt = receipt.get("attempt")
    if not isinstance(attempt, dict):
        return reasons

    if not attempt.get("id"):
        reasons.append("binding.attempt_id_missing")

    recorded = attempt.get("lock_digest")
    if lock_digest is not None and recorded != lock_digest:
        reasons.append("binding.lock_digest_mismatch")

    if "id" in lock and attempt.get("lock_id") != lock.get("id"):
        reasons.append("binding.lock_id_mismatch")

    started, finished = attempt.get("started_at"), attempt.get("finished_at")
    parsed = {}
    for name, value in (("started", started), ("finished", finished)):
        try:
            parsed[name] = datetime.datetime.strptime(value, TIMESTAMP)
        except (TypeError, ValueError):
            reasons.append("binding.attempt_time_format")
    if len(parsed) == 2 and parsed["finished"] < parsed["started"]:
        reasons.append("binding.attempt_stale")

    elapsed = attempt.get("elapsed_ms")
    if not (isinstance(elapsed, int) and not isinstance(elapsed, bool) and elapsed > 0):
        reasons.append("binding.elapsed_not_positive")

    return reasons


def check_observations(lock, receipt):
    """Return (defect reasons, unavailable reasons) for the mandatory observations."""
    defects, shortages = [], []
    observations = receipt.get("observations")
    if not isinstance(observations, dict):
        observations = {}

    for name in MANDATORY_OBSERVATIONS:
        observation = observations.get(name)
        if not isinstance(observation, dict):
            defects.append(f"observation.missing:{name}")
            continue

        for flag in ("performed", "proved", "timed_out", "cleaned"):
            if type(observation.get(flag)) is not bool:
                defects.append(f"observation.type:{name}.{flag}")
        performed = observation.get("performed") is True
        proved = observation.get("proved") is True
        timed_out = observation.get("timed_out") is True
        cleaned = observation.get("cleaned") is True

        if proved and (not performed or timed_out or not cleaned):
            defects.append(f"observation.contradiction:{name}")
        if not performed:
            defects.append(f"observation.not_performed:{name}")
        if not cleaned:
            defects.append(f"observation.not_cleaned:{name}")
        if performed and not proved:
            shortages.append(f"observation.unproved:{name}")

    return defects, shortages


def check_toolchain(lock, receipt):
    """Return the reasons the proved toolchain is not the locked one."""
    reasons = []
    observation = receipt.get("observations", {}).get("container_toolchain")
    if not isinstance(observation, dict) or not observation.get("proved"):
        return reasons

    expected_version = lock.get("toolchain", {}).get("kiwi", {}).get("upstream_version")
    reported = observation.get("reported_version")
    if reported is None:
        reasons.append("toolchain.version_missing")
    elif expected_version is not None and reported != expected_version:
        reasons.append("toolchain.version_mismatch")

    expected_tag = lock.get("invocation", {}).get("image_tag")
    if expected_tag is not None and observation.get("image_tag") != expected_tag:
        reasons.append("toolchain.image_tag_mismatch")

    return reasons


def check_accelerator(lock, receipt):
    """Return the reasons a proved accelerator observation is not a valid proof."""
    reasons = []
    observation = receipt.get("observations", {}).get("accelerator")
    if not isinstance(observation, dict) or not observation.get("proved"):
        return reasons

    preference = lock.get("probe", {}).get("accelerator_preference") or []
    selected = observation.get("selected")
    if not selected or selected not in preference:
        reasons.append("accelerator.unknown_selection")

    required = lock.get("probe", {}).get("required_qmp_status")
    status = observation.get("qmp_status")
    if status is None:
        reasons.append("accelerator.state_missing")
    elif required is not None and status != required:
        reasons.append("accelerator.state_mismatch")

    if observation.get("qmp_running") is not False:
        reasons.append("accelerator.vcpus_running")

    return reasons


def check_loop(lock, receipt):
    """Return the reasons a proved loop observation is not a valid proof."""
    reasons = []
    observation = receipt.get("observations", {}).get("loop_privilege")
    if not isinstance(observation, dict):
        return reasons
    if type(observation.get("mounted")) is not bool:
        reasons.append("observation.type:loop_privilege.mounted")
    if observation.get("proved") is True and observation.get("mounted") is not True:
        reasons.append("observation.contradiction:loop_privilege")
    return reasons


def check_capacity(lock, receipt):
    """Return (defect reasons, unavailable reasons) for the recorded capacity."""
    defects, shortages = [], []
    capacity = receipt.get("capacity")
    if not isinstance(capacity, dict):
        return defects, shortages

    if capacity.get("declared_build_measured"):
        defects.append("capacity.declared_labeled_measured")

    locked_allocation = lock.get("probe", {}).get("loop_image_bytes")
    if (
        locked_allocation is not None
        and capacity.get("probe_allocation_bytes") != locked_allocation
    ):
        defects.append("capacity.probe_allocation_mismatch")

    declared = lock.get("budget", {}).get("declared_build_bytes")
    if declared is not None and capacity.get("declared_build_bytes") != declared:
        defects.append("capacity.declared_budget_mismatch")

    available = capacity.get("evidence_avail_bytes")
    if isinstance(declared, int) and isinstance(available, int) and available < declared:
        shortages.append("capacity.insufficient_for_declared_budget")

    return defects, shortages


def check_architecture(lock, receipt):
    expected = lock.get("architecture", {}).get("uname_machine")
    observed = receipt.get("runner", {}).get("uname_machine")
    if expected is not None and observed is not None and expected != observed:
        return ["architecture.mismatch"]
    return []


def judge(lock, receipt, lock_digest=None, expected_context=None):
    """Classify a lock and a receipt. Pure: no host, environment or network is read."""
    defects = []
    shortages = []
    notes = [LIMIT_NOTE]

    defects += validate_lock(lock)
    defects += validate_receipt(receipt)
    if not isinstance(lock, dict) or not isinstance(receipt, dict):
        return Verdict("defect", dedupe(defects), notes)
    # Malformed containers must be refused without dereferencing their contents.
    for key in ("architecture", "toolchain", "toolchain.kiwi", "invocation", "probe", "budget"):
        if not isinstance(walk(lock, key)[1], dict):
            return Verdict("defect", dedupe(defects + ["lock.type:" + key]), notes)
    for key in ("attempt", "runner", "capacity", "observations"):
        if not isinstance(receipt.get(key), dict):
            return Verdict("defect", dedupe(defects + ["receipt.type:" + key]), notes)
    if not isinstance(receipt.get("refusals"), list):
        return Verdict("defect", dedupe(defects), notes)
    if expected_context is not None and not isinstance(expected_context, dict):
        return Verdict("defect", dedupe(defects + ["admission.context_type"]), notes)
    defects += check_binding(lock, receipt, lock_digest)
    defects += check_architecture(lock, receipt)
    if expected_context is not None:
        defects += check_admission(receipt, expected_context)
        defects += check_accelerator_history(lock, receipt)
    else:
        notes.append("Historical re-judgement only; a current expected context is required for recipe admission.")

    observation_defects, observation_shortages = check_observations(lock, receipt)
    defects += observation_defects
    shortages += observation_shortages

    defects += check_toolchain(lock, receipt)
    defects += check_accelerator(lock, receipt)
    defects += check_loop(lock, receipt)

    capacity_defects, capacity_shortages = check_capacity(lock, receipt)
    defects += capacity_defects
    shortages += capacity_shortages

    runner = receipt.get("runner")
    if isinstance(runner, dict) and "passwordless_sudo" in runner:
        if not runner["passwordless_sudo"]:
            shortages.append("runner.no_passwordless_sudo")

    accelerator = receipt.get("observations", {}).get("accelerator")
    if isinstance(accelerator, dict) and accelerator.get("selected") == "tcg":
        notes.append(TCG_NOTE)

    for refusal in receipt.get("refusals") or []:
        if isinstance(refusal, dict):
            notes.append(
                f"recorded refusal {refusal.get('name')}: {refusal.get('reason')}"
            )

    if defects:
        return Verdict("defect", dedupe(defects), notes)
    if shortages:
        return Verdict("unavailable", dedupe(shortages), notes)
    return Verdict("eligible", [], notes)


def check_accelerator_history(lock, receipt):
    """Bind a measured terminal proof to its bounded, ordered collection history."""
    reasons = []
    preference = walk(lock, "probe.accelerator_preference")[1]
    if preference not in (["kvm", "tcg"], ["tcg"], ["kvm"]):
        return ["accelerator.preference_invalid"]
    observation = walk(receipt, "observations.accelerator")[1]
    if not isinstance(observation, dict):
        return ["accelerator.observation_type"]
    attempts = observation.get("attempts")
    if not isinstance(attempts, list) or not 1 <= len(attempts) <= len(preference):
        return ["accelerator.attempts_missing_or_unbounded"]
    if observation.get("requested") != preference:
        reasons.append("accelerator.preference_mismatch")
    fields = ("selected", "performed", "proved", "timed_out", "cleaned",
              "qmp_status", "qmp_running", "elapsed_ms")
    for index, trial in enumerate(attempts):
        if not isinstance(trial, dict):
            reasons.append("accelerator.trial_type")
            continue
        for flag in ("performed", "proved", "timed_out", "cleaned"):
            if type(trial.get(flag)) is not bool:
                reasons.append("accelerator.trial_type:" + flag)
        if trial.get("selected") != preference[index] or trial.get("requested") != [preference[index]]:
            reasons.append("accelerator.trial_order")
        elapsed = trial.get("elapsed_ms")
        if type(elapsed) is not int or elapsed <= 0:
            reasons.append("accelerator.trial_elapsed")
        if trial.get("performed") is not True or trial.get("cleaned") is not True:
            reasons.append("accelerator.trial_unperformed_or_unclean")
        measured_proof = (trial.get("performed") is True
                          and trial.get("cleaned") is True
                          and trial.get("timed_out") is False
                          and trial.get("qmp_status") == walk(lock, "probe.required_qmp_status")[1]
                          and trial.get("qmp_running") is False)
        if trial.get("proved") is not measured_proof:
            reasons.append("accelerator.trial_proof_contradiction")
        if trial.get("proved") is True:
            if (trial.get("timed_out") is not False
                    or trial.get("qmp_status") != walk(lock, "probe.required_qmp_status")[1]
                    or trial.get("qmp_running") is not False):
                reasons.append("accelerator.trial_proof_invalid")
            if index != len(attempts) - 1:
                reasons.append("accelerator.fallback_after_success")
        # Failed attempts may have no QMP response. A present response is typed;
        # None records absence, not evidence of stopped virtual CPUs.
        if trial.get("qmp_running") is not None and type(trial.get("qmp_running")) is not bool:
            reasons.append("accelerator.trial_type:qmp_running")
        if trial.get("qmp_status") is not None and not isinstance(trial.get("qmp_status"), str):
            reasons.append("accelerator.trial_type:qmp_status")
    terminal = attempts[-1]
    if not isinstance(terminal, dict):
        return reasons
    if terminal.get("proved") is not True:
        reasons.append("accelerator.terminal_unproved")
    for field in fields:
        actual, measured = observation.get(field), terminal.get(field)
        if type(actual) is not type(measured) or actual != measured:
            reasons.append("accelerator.summary_mismatch:" + field)
    return reasons


def check_admission(receipt, expected):
    """The caller supplies current intent; a receipt must never mint its own permission."""
    reasons = []
    bindings = {
        "attempt_id": "attempt.id", "source_commit": "binding.source_commit",
        "lock_digest": "attempt.lock_digest", "build_input_digest": "binding.build_input_digest",
        "image_id": "observations.container_toolchain.image_id",
        "architecture": "runner.uname_machine", "accelerator": "observations.accelerator.selected",
    }
    for key, pointer in bindings.items():
        value = expected.get(key)
        if not isinstance(value, str) or not value or walk(receipt, pointer)[1] != value:
            reasons.append(f"admission.mismatch:{key}")
    for key in ("lock_digest", "build_input_digest"):
        if not BARE_DIGEST.fullmatch(str(expected.get(key, ""))):
            reasons.append(f"admission.digest:{key}")
    if not PREFIXED_DIGEST.fullmatch(str(expected.get("image_id", ""))):
        reasons.append("admission.image_id")
    if not re.fullmatch(r"[0-9a-f]{40}", str(expected.get("source_commit", ""))):
        reasons.append("admission.source_commit")
    # Reconstructed jobs must collect again, not copy a former run's context.
    for key in ("run_id", "run_attempt", "job"):
        if not expected.get(key) or walk(receipt, "binding." + key)[1] != expected[key]:
            reasons.append(f"admission.mismatch:{key}")
    if walk(receipt, "capacity.after_toolchain")[1] is not True:
        reasons.append("admission.capacity_not_current")
    return reasons


def dedupe(reasons):
    """Preserve order and drop repeats, so a reason list reads as a set of findings."""
    seen, ordered = set(), []
    for reason in reasons:
        if reason not in seen:
            seen.add(reason)
            ordered.append(reason)
    return ordered


def main(argv=None):
    parser = argparse.ArgumentParser(
        description="Classify an appliance image toolchain prerequisite receipt."
    )
    parser.add_argument("--lock", required=True, help="path to the checked-in input lock")
    parser.add_argument("--receipt", required=True, help="path to the collected receipt")
    parser.add_argument("--expected-context", help="current recipe intent; omit only for historical audit")
    arguments = parser.parse_args(argv)

    try:
        with open(arguments.lock, "rb") as handle:
            lock = json.loads(handle.read().decode("utf-8"))
        lock_digest = sha256_file(arguments.lock)
    except (OSError, ValueError, UnicodeDecodeError) as error:
        return refuse(f"the lock could not be read as JSON: {error}")

    try:
        with open(arguments.receipt, "rb") as handle:
            receipt = json.loads(handle.read().decode("utf-8"))
    except (OSError, ValueError, UnicodeDecodeError) as error:
        return refuse(f"the receipt could not be read as JSON: {error}")

    if not isinstance(lock, dict) or not isinstance(receipt, dict):
        return refuse("the lock and the receipt must both be JSON objects")

    expected = None
    if arguments.expected_context:
        try:
            with open(arguments.expected_context, encoding="utf-8") as handle:
                expected = json.load(handle)
            if not isinstance(expected, dict):
                return refuse("the expected admission context must be an object")
        except (OSError, ValueError) as error:
            return refuse(f"the expected context could not be read: {error}")
    verdict = judge(lock, receipt, lock_digest, expected)
    print(json.dumps(verdict.payload(), indent=2, sort_keys=True))
    for reason in verdict.reasons:
        print(f"{verdict.state}: {reason}", file=sys.stderr)
    return verdict.exit_code


def refuse(message):
    """An input that cannot be judged is a defect, never a pass."""
    payload = {"state": "defect", "reasons": ["input.unreadable"], "notes": [message]}
    print(json.dumps(payload, indent=2, sort_keys=True))
    print(f"defect: {message}", file=sys.stderr)
    return EXIT_BY_STATE["defect"]


if __name__ == "__main__":
    sys.exit(main())
