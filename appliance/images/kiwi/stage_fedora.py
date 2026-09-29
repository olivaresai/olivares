#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""stage_fedora.py — the Fedora stage step after the builder's dracut modules are staged: the in-build dracut RPM in a
signed repository of its own. Run by build.sh inside the admitted Fedora builder; every path is an argument.

  1. dracut_package.py builds dracut-kiwi-oem-dump at the locked KIWI version from ROOT's modules into BUILD_PACKAGES.
  2. A per-build OpenPGP key (ed25519, one day) is made in a private GnuPG home outside the build's directories.
  3. D's S3 scripts sign it: rpm-payload-sign.py --rpm PACKAGE, then render-rpm-repodata.py render --repo BUILD_PACKAGES
     (createrepo_c and repodata/repomd.xml.asc), with a key descriptor of environment `test` and the test-only latch
     OLIVARES_PACKAGE_REPO_TEST_ONLY=1 the scripts require for such a key.
  4. The index check (dracut_package.py --format rpm --indexed) finds the package at the locked version.
  5. DESCRIPTION gets the public key (olivares-build-key.asc) and olivares-build-repository.json, which pins its
     fingerprint for olivares-repo-pinned.sh; the secret key and the GnuPG home are removed.
exit 0 done; exit 1 a step failed (its output is shown); exit 2 usage."""
import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

sys.path.insert(0, str(Path(__file__).resolve().parent))
import dracut_package  # noqa: E402

KEY_SCHEMA = "olivares.ai/package-repository-key/v1"


def step(argv, env=None, echo=True):
    """Run one step and return its output; the output is echoed to the build log unless ECHO is false, as for the
    exported keys (the secret half never reaches the log, which the image workflow keeps as evidence)."""
    result = subprocess.run(argv, capture_output=True, text=True, timeout=600, env=env)
    if echo:
        sys.stdout.write(result.stdout)
    sys.stderr.write(result.stderr[-4000:])
    if result.returncode != 0:
        print("stage_fedora.py: %s exited %d" % (argv[:3], result.returncode), file=sys.stderr)
        raise SystemExit(1)
    return result.stdout


def main(argv):
    parser = argparse.ArgumentParser()
    for name in ("--lock", "--root", "--build-packages", "--description", "--s3"):
        parser.add_argument(name, required=True)
    try:
        options = parser.parse_args(argv)
    except SystemExit:
        return 2
    try:
        dracut_package.build_rpm(options.lock, options.root, options.build_packages)
    except (dracut_package.Refused, dracut_package.Unable) as issue:
        print("stage_fedora.py: %s" % issue, file=sys.stderr)
        return 1
    built = sorted(Path(options.build_packages).glob("*.rpm"))
    if len(built) != 1:
        print("stage_fedora.py: the build repository must hold exactly the dracut RPM, it holds %s" % built, file=sys.stderr)
        return 1
    private = Path(tempfile.mkdtemp(prefix="k."))
    home = Path(tempfile.mkdtemp(prefix="g."))
    try:
        private.chmod(0o700)
        home.chmod(0o700)
        gpg = ["gpg", "--homedir", str(home), "--batch", "--no-tty", "--pinentry-mode", "loopback", "--passphrase", ""]
        step(gpg + ["--quick-generate-key", "Olivares appliance per-build key <per-build@invalid.olivares.ai>",
                    "ed25519", "sign", "1d"])
        listed = step(gpg + ["--with-colons", "--list-secret-keys"])
        fingerprint = next(line.split(":")[9] for line in listed.splitlines() if line.startswith("fpr:"))
        secret = private / "per-build.key"
        secret.touch(0o600)
        secret.write_text(step(gpg + ["--armor", "--export-secret-keys", fingerprint], echo=False))
        public = step(gpg + ["--armor", "--export", fingerprint], echo=False)
        descriptor = private / "descriptor.json"
        descriptor.write_text(json.dumps({"schema": KEY_SCHEMA, "purpose": "apt-rpm-apk-repository-metadata",
                                          "environment": "test", "openpgp_fingerprint": fingerprint,
                                          "apk_public_key_name": "not-used-by-rpm",
                                          "apk_public_key_sha256": "0" * 64}))
        env = dict(os.environ, OLIVARES_PACKAGE_REPO_TEST_ONLY="1", OLIVARES_PACKAGE_REPO_KEY_DESCRIPTOR_FILE=str(descriptor),
                   OLIVARES_PACKAGE_REPO_OPENPGP_SECRET_KEY_FILE=str(secret), TMPDIR=str(private))
        s3 = Path(options.s3)
        step([sys.executable, str(s3 / "rpm-payload-sign.py"), "--rpm", str(built[0])], env)
        step([sys.executable, str(s3 / "render-rpm-repodata.py"), "render", "--repo", options.build_packages], env)
        try:
            dracut_package.indexed_rpm(options.lock, options.build_packages)
        except (dracut_package.Refused, dracut_package.Unable) as issue:
            print("stage_fedora.py: %s" % issue, file=sys.stderr)
            return 1
        description = Path(options.description)
        (description / "olivares-build-key.asc").write_text(public)
        (description / "olivares-build-repository.json").write_text(json.dumps(
            {"olivares-appliance-build": {"key": "olivares-build-key.asc", "fingerprint": fingerprint}}, indent=2) + "\n")
        print("stage_fedora.py: %s signed with the per-build key %s; its secret key is discarded" % (built[0].name,
                                                                                                    fingerprint))
    finally:
        subprocess.run(["gpgconf", "--homedir", str(home), "--kill", "all"], capture_output=True, timeout=30)
        shutil.rmtree(home, ignore_errors=True)
        shutil.rmtree(private, ignore_errors=True)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
