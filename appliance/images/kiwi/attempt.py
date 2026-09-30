#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Own one hosted preflight, image build and boot attempt; always retire its builder."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import select
import signal
import subprocess
import sys
import threading
import time

ROOT = Path(__file__).resolve().parents[3]
RUNNER = ROOT / "appliance/test/runner"
sys.path.insert(0, str(RUNNER))
sys.path.insert(0, str(Path(__file__).resolve().parent))
from collector import atomic_json, now
from consume import attempt_id, image_id, same_job
from delivery_check import fingerprint
from judge import walk
from owned_resources import LABEL, release_container, run
from workflow import execute as execute_preflight

LOCK = ROOT / "appliance/images/toolchain/input-lock.json"
# The builder of each base: the preflight qualifies, and the release retires, the builder the attempt's base names. The
# base is recorded in the attempt (recipe-base.json), so a later --release-only uses the same lock.
LOCKS = {"fedora44": ROOT / "appliance/images/toolchain/fedora44/input-lock.json", "debian13": LOCK}
# A phase log keeps the first LOG_HEAD bytes and, beyond them, the last LOG_TAIL bytes.
LOG_HEAD = 16 * 1024 * 1024
LOG_TAIL = 256 * 1024
# Free disk is sampled, and one progress line printed, every SAMPLE_SECONDS; at most
# SAMPLE_LIMIT samples are written, later ones are only counted.
SAMPLE_SECONDS = 30
SAMPLE_LIMIT = 2000
# The evidence the workflow retains and SHA256SUMS covers: text records and guest timing,
# command and stderr files; never a disk image or firmware variable store.
RETAINED_SUFFIXES = (".json", ".jsonl", ".log", ".txt")
RETAINED_NAMES = ("seconds", "qemu.cmd", "qemu.stderr", "ready", "product.active")
# The same environment owned_resources.run passes: no secret and no host variable reaches a phase.
PHASE_PATH = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
PHASE_METADATA = ("GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT", "GITHUB_JOB", "ImageOS", "ImageVersion", "HOME")
CIDFILES = ("stage.cid", "build.cid")
# The first release record keeps its documented places; later releases never touch them.
FIRST_RECORD = ("cleanup.json", "release.log", "release.log.json", "prerequisite/builder-release.json")


def run_command(argv, log, timeout):
    """Run one command in its own process group, streaming its output into LOG as it arrives.

    A hard kill keeps what was already written. The raw exit, times and interruption are
    recorded beside it in LOG.json, first as running and then as finished."""
    log, record_path = Path(log), Path(str(log) + ".json")
    record = {"command": [str(part) for part in argv], "timeout_seconds": timeout,
              "started_at": now(), "state": "running"}
    atomic_json(record_path, record)
    environment = {"PATH": PHASE_PATH, "LANG": "C.UTF-8", "DOCKER_BUILDKIT": "1"}
    environment.update((key, os.environ[key]) for key in PHASE_METADATA if key in os.environ)
    clock = time.monotonic()
    deadline = clock + timeout
    child, code, timed_out, interrupted = None, 1, False, False
    written, beyond, tail = 0, 0, b""
    with log.open("xb") as output:  # A log is evidence: never overwritten.
        try:
            child = subprocess.Popen(record["command"], stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                     env=environment, start_new_session=True)
            while True:
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    timed_out = True
                    break
                ready, _, _ = select.select([child.stdout], [], [], min(remaining, 1.0))
                if not ready:
                    if child.poll() is not None:
                        break
                    continue
                chunk = os.read(child.stdout.fileno(), 65536)
                if not chunk:
                    break
                kept = chunk[:max(0, LOG_HEAD - written)]
                output.write(kept)
                output.flush()
                written += len(kept)
                if len(kept) < len(chunk):
                    beyond += len(chunk) - len(kept)
                    tail = (tail + chunk[len(kept):])[-LOG_TAIL:]
            if not timed_out:
                child.wait(timeout=max(0.0, deadline - time.monotonic()))
        except subprocess.TimeoutExpired:
            timed_out = True
        except KeyboardInterrupt:
            interrupted = True
        except OSError as error:
            output.write(("command did not start: %s\n" % error).encode())
            code = 127
        finally:
            if child is not None:
                for sig in (signal.SIGTERM, signal.SIGKILL):
                    if child.poll() is not None:
                        break
                    try:
                        os.killpg(child.pid, sig)
                        child.wait(timeout=2)
                    except (ProcessLookupError, subprocess.TimeoutExpired):
                        pass
                child.stdout.close()
                code = 124 if timed_out else (child.returncode if child.returncode is not None else 1)
            if beyond:
                output.write(b"\n[%d bytes not retained; the last %d bytes follow]\n" % (beyond - len(tail), len(tail)) + tail)
            record.update(state="finished", exit=code, timed_out=timed_out, interrupted=interrupted,
                          joined=child is None or child.poll() is not None, finished_at=now(),
                          duration_seconds=round(time.monotonic() - clock, 3),
                          output_bytes=written + beyond, dropped_bytes=beyond - len(tail))
            atomic_json(record_path, record)
    if interrupted:
        raise KeyboardInterrupt("recipe interrupted during " + log.name)
    return code if record["joined"] else 1


class Watch:
    """Sample free disk and print one bounded progress line per interval while the attempt runs."""

    LIMITATION = ("Free bytes are sampled on the filesystems of /, the evidence and the target directory "
                  "at each phase start and every interval. Use between samples, on other filesystems or "
                  "after the last sample is not observed: the largest sampled drop from the first sample "
                  "is a lower bound of peak use, not the peak.")

    def __init__(self, evidence, paths):
        self.samples = evidence / "disk-samples.jsonl"
        self.paths = paths
        self.interval = SAMPLE_SECONDS
        self.phase = ("setup", None, time.monotonic())
        self.first, self.lowest, self.count, self.errors = {}, {}, 0, []
        self.lock = threading.Lock()
        self.stopped = threading.Event()
        self.thread = threading.Thread(target=self.loop, name="appliance-attempt-watch", daemon=True)

    def enter(self, name, log=None):
        self.phase = (name, log, time.monotonic())
        print("appliance-attempt: phase=%s started_at=%s" % (name, now()), flush=True)
        self.sample()

    def sample(self):
        row = {"at": now(), "phase": self.phase[0], "free_bytes": {}}
        try:
            for name, path in self.paths.items():
                while not path.exists():
                    path = path.parent
                disk = os.statvfs(path)
                row["free_bytes"][name] = disk.f_bavail * disk.f_frsize
        except OSError as error:
            with self.lock:
                self.errors = (self.errors + [str(error)])[-5:]
            return row
        with self.lock:
            self.count += 1
            for name, free in row["free_bytes"].items():
                self.first.setdefault(name, free)
                self.lowest[name] = min(free, self.lowest.get(name, free))
            if self.count <= SAMPLE_LIMIT:
                with self.samples.open("a", encoding="utf-8") as output:
                    output.write(json.dumps(row, sort_keys=True) + "\n")
        return row

    def loop(self):
        while not self.stopped.wait(self.interval):
            row = self.sample()
            name, log, started = self.phase
            size, last = 0, ""
            try:
                if log is not None and log.is_file():
                    size = log.stat().st_size
                    with log.open("rb") as handle:
                        handle.seek(max(0, size - 4096))
                        lines = handle.read().decode("utf-8", "replace").strip().splitlines()
                    last = "".join(c for c in (lines[-1] if lines else "") if c.isprintable())[:160]
            except OSError as error:
                last = "log unreadable: %s" % error
            free = " ".join("%s=%d" % item for item in sorted(row["free_bytes"].items()))
            print("appliance-attempt: phase=%s elapsed=%ds output=%dB free_bytes: %s last=%r"
                  % (name, time.monotonic() - started, size, free, last), flush=True)

    def start(self):
        self.thread.start()

    def stop(self):
        self.stopped.set()
        self.thread.join(timeout=5)
        with self.lock:
            return {"interval_seconds": self.interval, "samples": self.count,
                    "samples_written": min(self.count, SAMPLE_LIMIT), "first_free_bytes": self.first,
                    "lowest_free_bytes": self.lowest,
                    "largest_sampled_drop_bytes": {k: self.first[k] - self.lowest[k] for k in self.first},
                    "sample_errors": self.errors, "limitation": self.LIMITATION}


def timed(phases, watch, name, log, call):
    """Record one phase's start, finish, duration and raw exit, including when it raises."""
    watch.enter(name, log)
    entry = {"name": name, "started_at": now()}
    phases.append(entry)
    clock = time.monotonic()
    try:
        entry["exit"] = call()
        return entry["exit"]
    except BaseException as error:
        entry["raised"] = type(error).__name__
        raise
    finally:
        entry.update(finished_at=now(), duration_seconds=round(time.monotonic() - clock, 3))
        print("appliance-attempt: phase=%s finished_at=%s exit=%s duration=%ss" % (
            name, entry["finished_at"], entry.get("exit", entry.get("raised")), entry["duration_seconds"]), flush=True)


def bound_attempt(receipt):
    """This job's attempt ID from a receipt; a receipt of another run, attempt or job refuses."""
    same_job(receipt["binding"], os.environ)
    return attempt_id(receipt["attempt"]["id"])


def cleanup_containers(evidence, attempt, observed=None):
    """Retire the exact containers this recipe recorded; refuse and keep everything else.

    A cidfile must hold one exact ID. It may name a container that is already absent, as after
    an earlier release, but never one another owner labeled. A labeled container without a
    cidfile is unknown work: nothing is deleted and custody stays unconfirmed."""
    observed = {} if observed is None else observed
    try:
        labeled = run(["docker", "container", "ls", "--all", "--no-trunc", "--quiet", "--filter", "label=" + LABEL + "=" + attempt])
        present = run(["docker", "container", "ls", "--all", "--no-trunc", "--quiet"])
        if any(r.code or r.timed_out or not r.joined for r in (labeled, present)):
            observed["refused"] = "container inventory not observed"
            return False
        live, every = set(labeled.output.split()), set(present.output.split())
        owned = set()
        for name in CIDFILES:
            path = evidence / "recipe-work" / name
            if path.exists():
                value = path.read_text().strip()
                if not re.fullmatch(r"[0-9a-f]{64}", value):
                    observed["refused"] = name + " does not hold one exact container ID"
                    return False
                if value in every and value not in live:
                    observed["refused"] = name + " names a container another owner labeled"
                    return False
                owned.add(value)
        observed.update(recorded=sorted(owned), labeled=sorted(live))
        if live - owned:
            observed["refused"] = "labeled containers without a cidfile: " + " ".join(sorted(live - owned))
            return False
        observed["released"] = []
        for container in sorted(live):
            if not release_container(run, container, attempt):
                observed["refused"] = "release not observed for " + container
                return False
            observed["released"].append(container)
        return True
    except (OSError, ValueError, KeyError, TypeError) as error:
        observed["refused"] = str(error)
        return False


def publish(path, value):
    """Write a JSON record exactly once; an existing record is evidence and is never replaced."""
    temporary = path.with_name(path.name + ".new")
    with temporary.open("x", encoding="utf-8") as output:
        json.dump(value, output, indent=2, sort_keys=True)
        output.write("\n")
        output.flush()
        os.fsync(output.fileno())
    try:
        os.link(temporary, path)
    finally:
        temporary.unlink()


def claim(evidence):
    """Return the directory of this release's record.

    The first release writes cleanup.json, release.log and the prerequisite's
    builder-release.json at their documented places. Every later release, such as the
    workflow's always step, claims a new recovery/NNN by an exclusive mkdir."""
    if not any((evidence / name).exists() for name in FIRST_RECORD):
        return evidence
    recovery = evidence / "recovery"
    recovery.mkdir(exist_ok=True)
    number = max((int(p.name) for p in recovery.iterdir() if p.name.isdigit()), default=0)
    while True:
        number += 1
        try:
            (recovery / ("%03d" % number)).mkdir()
            return recovery / ("%03d" % number)
        except FileExistsError:
            continue


def seal(evidence, record=None):
    """Write SHA256SUMS once, with attempt-relative paths: the attempt's or a recovery record's.

    A record's manifest also hashes the manifest before it (the previous sealed record's, or
    the attempt's), so the latest record commits to the whole chain."""
    directory = record or evidence
    files = [p for p in directory.rglob("*") if p.is_file() and (p.suffix in RETAINED_SUFFIXES or p.name in RETAINED_NAMES)]
    if record is not None:
        earlier = sorted(p for p in record.parent.glob("*/SHA256SUMS") if p.parent.name < record.name)
        previous = earlier[-1] if earlier else evidence / "SHA256SUMS"
        if previous.exists():
            files.append(previous)
    rows = "".join(hashlib.sha256(p.read_bytes()).hexdigest() + "  " + p.relative_to(evidence).as_posix() + "\n"
                   for p in sorted(files))
    with (directory / "SHA256SUMS").open("x", encoding="utf-8") as output:
        output.write(rows)


def released_before(evidence, attempt, image):
    """The prerequisite receipt of an earlier release that observed this exact image removed."""
    for path in [evidence / "prerequisite/builder-release.json", *sorted(evidence.glob("recovery/*/builder-release.json"))]:
        if path.exists():
            previous = json.loads(path.read_bytes())
            if (isinstance(previous, dict) and previous.get("state") == "released" and previous.get("image_removed") is True
                    and previous.get("attempt_id") == attempt and previous.get("image_id") == image):
                return path.relative_to(evidence).as_posix()
    return None


def typed_custody(receipt):
    """The builder custody of a receipt, read as the prerequisite reads its own receipts.

    Every container must be an object and each custody flag the release decision reads an exact
    boolean: image_retained and image_removed always, cleaned when no image was retained. 0/1,
    strings, null, lists or a missing flag prove nothing. A refusal raises before any effect."""
    for pointer in ("attempt", "binding", "observations", "observations.container_toolchain"):
        if not isinstance(walk(receipt, pointer)[1], dict):
            raise ValueError("receipt.type:" + pointer)
    toolchain = receipt["observations"]["container_toolchain"]
    flags = ("image_retained", "image_removed") + (() if toolchain.get("image_retained") is True else ("cleaned",))
    for flag in flags:
        if type(toolchain.get(flag)) is not bool:
            raise ValueError("receipt.type:observations.container_toolchain." + flag)
    if toolchain["image_retained"] and toolchain["image_removed"]:
        raise ValueError("receipt.custody: an image cannot be both retained and removed")
    return toolchain


def settle(evidence, record, outcome):
    """Perform one release and fill OUTCOME after validating prerequisite custody.

    Effects, in order: stop and remove this recipe's recorded child containers
    (cleanup_containers); then, only for a builder image the prerequisite retained for this
    job and no earlier record saw removed, write this record's receipt copy (recovery only)
    and run admit.py --release, which removes the exact image and writes builder-release.json
    beside the receipt it reads. A builder the prerequisite did not retain is not touched;
    its own cleaned flag decides whether custody is confirmed. A malformed earlier
    release record can raise after child cleanup and before the image release."""
    path = evidence / "prerequisite/receipt.json"
    if not path.exists():
        # No recorded identity authorizes removal; a killed collector may own work.
        raise ValueError("prerequisite receipt absent; cleanup unconfirmed")
    receipt = json.loads(path.read_bytes())
    toolchain = typed_custody(receipt)
    attempt = bound_attempt(receipt)
    image = image_id(toolchain.get("image_id")) if toolchain["image_retained"] else None
    containers = {}
    clean = cleanup_containers(evidence, attempt, containers)
    outcome.update(containers_released=clean, containers=containers)
    if not toolchain["image_retained"]:
        # The prerequisite kept no image for this job, even when it could not name one.
        confirmed = toolchain["cleaned"]
        outcome.update(image="released_by_prerequisite" if toolchain["image_removed"] else "never_retained",
                       prerequisite_cleaned=confirmed, state="released" if clean and confirmed else "defect")
        return
    prior = released_before(evidence, attempt, image)
    if prior:
        outcome.update(image="already_released", image_release=prior, state="released" if clean else "defect")
        return
    if record != evidence:
        # admit.py writes builder-release.json beside the receipt it reads. This record's own
        # byte copy keeps that result here and leaves every earlier record unchanged.
        with (record / "receipt.json").open("xb") as copy:
            copy.write(path.read_bytes())
        path = record / "receipt.json"
    # The exact image release is always attempted even when a child could not be
    # retired. The prerequisite refuses an image still in use; uncertainty stays red.
    code = run_command([sys.executable, str(RUNNER / "admit.py"), "--lock", str(attempt_lock(evidence)),
        "--receipt", str(path), "--attempt", attempt, "--image-id", image, "--release"],
        record / "release.log", 90)
    outcome.update(image="released" if code == 0 else "release_not_confirmed", release_exit=code,
                   image_release=(path.parent / "builder-release.json").relative_to(evidence).as_posix(),
                   state="released" if code == 0 and clean else "defect")


def attempt_lock(evidence):
    """The lock of the base this attempt recorded; an attempt without a record is the Debian builder's, as before."""
    try:
        return LOCKS[json.loads((evidence / "recipe-base.json").read_bytes())["base"]]
    except OSError:
        return LOCK


def release(evidence):
    """Retire this attempt's children and exact image, and add one release record."""
    record, outcome = None, {"state": "defect"}
    try:
        record = claim(evidence)
        outcome["record"] = record.relative_to(evidence).as_posix()
        settle(evidence, record, outcome)
    except KeyboardInterrupt:
        outcome["reason"] = "release interrupted; cleanup unconfirmed"
    except (OSError, ValueError, KeyError, TypeError) as error:
        outcome["reason"] = str(error)
    outcome["observed_at"] = now()
    if outcome["state"] == "defect":
        outcome.setdefault("reason", "child or image release not confirmed")
    try:
        if record is not None:
            publish(record / "cleanup.json", outcome)
            if record != evidence:
                seal(evidence, record)
    except OSError as error:
        outcome.update(state="defect", reason="release record not written: %s" % error)
    print("appliance-attempt: release record %s state=%s" % (outcome.get("record", "unclaimed"), outcome["state"]), flush=True)
    return 0 if outcome["state"] == "released" else 1


def execute(evidence, accelerator, packages, target, firmware, base="fedora44", edition="server",
            qualification_key=None):
    """One attempt on BASE's builder. PACKAGES is the product repository D's S3 delivers (Fedora) or the directory of
    the product's .debs (Debian); QUALIFICATION_KEY names a throwaway package-repository key, so the build is not a
    release: its OpenPGP fingerprint, or the armored key file qualification-delivery.sh writes, whose fingerprint the
    build is given."""
    evidence = Path(evidence).resolve()
    if accelerator not in ("kvm", "tcg") or firmware not in ("free", "nonfree") or base not in LOCKS:
        return 2
    # The desktop edition is the Fedora 44 server profile plus GNOME; no other base builds it.
    if edition not in ("server", "desktop") or (edition == "desktop" and base != "fedora44"):
        return 2
    # The build names its outputs from VERSION alone: X.Y.Z for a release, 0.0.0-dev (also when unset) for a
    # qualification. Anything else stops here, before the preflight builds its container.
    version = os.environ.get("VERSION", "0.0.0-dev")
    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+|0\.0\.0-dev", version):
        print("appliance-attempt: VERSION is X.Y.Z or 0.0.0-dev, not %r" % version)
        return 2
    if qualification_key and not re.fullmatch(r"[0-9A-F]{40}", qualification_key):
        try:
            qualification_key = fingerprint(Path(qualification_key).read_bytes())
        except (OSError, ValueError, IndexError) as error:
            print("appliance-attempt: --qualification-key is neither a fingerprint nor a key file: %s" % error)
            return 2
    try:
        evidence.mkdir()  # Exclusive owner: no reused attempt or receipt overwrite.
    except OSError:
        return 2
    lock = LOCKS[base]
    atomic_json(evidence / "recipe-base.json", {"base": base, "lock": str(lock.relative_to(ROOT))})
    result = {"state": "defect", "phase": "preflight", "started_at": now(), "phases": []}
    code = 1
    watch = Watch(evidence, {"root": Path("/"), "evidence": evidence, "target": Path(target).resolve()})
    watch.start()
    try:
        preflight = evidence / "prerequisite"
        code = timed(result["phases"], watch, "preflight", preflight / "preflight.log",
            lambda: execute_preflight(preflight, [sys.executable, str(RUNNER / "collector.py"),
                "--lock", str(lock), "--evidence", str(preflight), "--retain-builder"]))
        if code == 0:
            receipt = json.loads((preflight / "receipt.json").read_bytes())
            attempt = bound_attempt(receipt)
            image = image_id(receipt["observations"]["container_toolchain"]["image_id"])
            common = ["env", "PYTHONDONTWRITEBYTECODE=1", "APPLIANCE_BOOT_ACCEL=" + accelerator,
                      "APPLIANCE_EDITION=" + edition, "VERSION=" + version]
            phases = [("build", 5400, common + ["WORK_DIR=" + str(evidence / "recipe-work"),
                "bash", str(ROOT / "appliance/images/kiwi/build.sh"), "--receipt", str(preflight / "receipt.json"),
                "--attempt", attempt, "--image-id", image, "--accelerator", accelerator, "--base", base,
                "--edition", edition,
                "--deb-dir" if base == "debian13" else "--package-dir", str(Path(packages).resolve()),
                "--target-dir", str(Path(target).resolve()), "--firmware", firmware]
                + (["--qualification-key", qualification_key] if qualification_key else []))]
            phases += [("format-" + fmt, 600, common + ["bash", str(ROOT / "appliance/images/formats/assemble.sh"),
                "--format", fmt, "--edition", edition, "--target-dir", str(Path(target).resolve()),
                "--output-dir", str(Path(target).resolve())])
                for fmt in ("iso", "qcow2", "ova")]
            phases += [("boot", 5700, common + ["bash", str(ROOT / "appliance/test/boot-battery.sh"),
                str(Path(target).resolve()), str(evidence / "boot"), "bios", "uefi", "uefi-secureboot", "iso-install"]),
                ("nocloud", 1350, common + ["bash", str(ROOT / "appliance/test/nocloud-probe.sh"),
                str(Path(target).resolve()), str(evidence / "nocloud")])]
            # The first failing phase stops the recipe and is its answer, with one exception: the NoCloud
            # battery boots the same finished images as the boot battery, with its own seed and its own
            # assertions, so after a boot battery that measured and failed (exit 1) it still runs and
            # its evidence is kept. A boot battery that could not measure (2) or did not finish stops
            # the recipe there, as any other failure does.
            failed = None
            for phase, timeout, argv in phases:
                if failed and not (phase == "nocloud" and failed == ("boot", 1)):
                    break
                if not failed:
                    result["phase"] = phase
                log = evidence / (phase + ".log")
                code = timed(result["phases"], watch, phase, log,
                             lambda argv=argv, log=log, timeout=timeout: run_command(argv, log, timeout))
                if code and not failed:
                    failed = (phase, code)
            code = failed[1] if failed else 0
            result.update(attempt_id=attempt, image_id=image, accelerator=accelerator)
    except (OSError, ValueError, KeyError, TypeError, KeyboardInterrupt) as error:
        result["reason"] = str(error)
        code = 1
    finally:
        watch.enter("release")
        release_code = release(evidence)
        # Three answers: 2 only when a phase classified a missing prerequisite; any other
        # failure, including a timeout (124) or a missing command (127), is a defect.
        recipe_code = code if code in (0, 2) else 1
        result.update(exit=1 if release_code else recipe_code, recipe_exit=recipe_code, phase_exit=code,
                      release_exit=release_code, finished_at=now(), disk=watch.stop())
        result["state"] = "complete" if result["exit"] == 0 else ("unavailable" if result["exit"] == 2 else "defect")
        atomic_json(evidence / "recipe-result.json", result)
        seal(evidence)
    return result["exit"]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--evidence", required=True)
    parser.add_argument("--accelerator", choices=("kvm", "tcg"))
    parser.add_argument("--base", choices=sorted(LOCKS), default="fedora44")
    parser.add_argument("--edition", choices=("server", "desktop"), default="server")
    parser.add_argument("--package-dir", default=None)
    parser.add_argument("--deb-dir", default=None)
    parser.add_argument("--qualification-key", default="")
    parser.add_argument("--target-dir", default="dist/appliance")
    parser.add_argument("--firmware", choices=("free", "nonfree"), default="nonfree")
    parser.add_argument("--release-only", action="store_true")
    args = parser.parse_args()
    evidence = Path(args.evidence).resolve()
    if args.release_only:
        if not evidence.exists():
            print("no recipe attempt directory; nothing was started")
            return 0
        code = release(evidence)
        if not (evidence / "SHA256SUMS").exists():
            # The attempt was stopped before its own finally: seal what it left, once.
            seal(evidence)
        return code
    if args.accelerator is None:
        parser.error("--accelerator is required")
    if args.deb_dir is not None and args.base != "debian13":
        parser.error("--deb-dir names .debs, for --base debian13 only; the Fedora base reads --package-dir")
    if args.edition == "desktop" and args.base != "fedora44":
        parser.error("--edition desktop is built on the fedora44 base only")
    packages = args.package_dir or args.deb_dir or ("dist" if args.base == "debian13" else "dist/rpm")
    return execute(evidence, args.accelerator, packages, args.target_dir, args.firmware, base=args.base,
                   edition=args.edition, qualification_key=args.qualification_key or None)


if __name__ == "__main__":
    def interrupted(signum, frame):
        raise KeyboardInterrupt("recipe interrupted")
    signal.signal(signal.SIGTERM, interrupted)
    sys.exit(main())
