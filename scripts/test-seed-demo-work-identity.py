#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Regression battery for scripts/seed-demo-work.py: a provider profile is resolved by its
IMMUTABLE identity, never by its label.

After a 409 on create (the demo home already has a profile), the seeder must walk every page
of the tenant's active profiles, confirm each local+operable candidate through the authorized
configuration read (driver, environment, canonical config_home and user_home), and launch only
with EXACTLY one exact identity — re-read as active/local/operable immediately before the run.

Exit 0 when every case passes, 1 on a failure, 2 when the battery could not look. No server,
account, provider or non-local endpoint: the API is a local fake wired through `pedir`.

    python3 scripts/test-seed-demo-work-identity.py
"""

from __future__ import annotations

import importlib.util
import os
import sys
import tempfile
import unittest
from pathlib import Path

SEEDER = Path(__file__).resolve().with_name("seed-demo-work.py")


def load_seeder():
    spec = importlib.util.spec_from_file_location("seed_demo_work_under_test", SEEDER)
    if spec is None or spec.loader is None:
        print("NO HE PODIDO MIRAR: %s no se puede cargar" % SEEDER, file=sys.stderr)
        sys.exit(2)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class FakeAPI:
    """One demo estate behind `pedir`. `profiles` is the list the tenant holds: each entry is a
    list item plus its authorized configuration (`config`) and whether the configuration read
    is authorized for this actor (`config_authorized`)."""

    def __init__(self, root: str, pages: list[list[dict]], profiles: dict[str, dict],
                 create_status: int = 409):
        self.root = root
        self.pages = pages
        self.profiles = profiles
        self.create_status = create_status
        self.list_paths: list[str] = []
        self.config_paths: list[str] = []
        self.run_profile_refs: list[str] = []

    def __call__(self, base, token, tenant, method, path, body=None, cabeceras=None):
        del base, token, tenant, cabeceras
        if method == "POST" and path == "/v1/m/sessions/workspaces":
            return 201, {"workspace_ref": "ws_test"}
        if method == "GET" and path == "/v1/m/sessions/workspaces/ws_test":
            return 200, {"state": "active", "mount_mode": "ro", "root_path": self.root}
        if method == "GET" and path.startswith("/v1/m/sessions/workspaces/ws_test/files"):
            return 200, {"entries": [{"name": n} for n in ("README.md", "deploy", "src")]}
        if method == "POST" and path == "/v1/m/sessions/provider-profiles":
            if self.create_status == 201:
                return 201, {"profile_ref": "ppf_created"}
            return self.create_status, {"error": "an active or disabled profile already owns this home"}
        if method == "GET" and path.startswith("/v1/m/sessions/provider-profiles?"):
            self.list_paths.append(path)
            index = 0
            if "cursor=" in path:
                index = int(path.rsplit("cursor=page-", 1)[-1]) - 1
            items = self.pages[index] if index < len(self.pages) else []
            more = index + 1 < len(self.pages)
            page = {"items": items, "has_more": more}
            if more:
                page["cursor"] = "page-%d" % (index + 2)
            return 200, page
        if method == "GET" and path.endswith("/configuration") and "/provider-profiles/" in path:
            ref = path.rsplit("/", 2)[-2]
            self.config_paths.append(ref)
            prof = self.profiles.get(ref)
            if prof is None:
                return 404, {"error": "not found"}
            if not prof.get("config_authorized", True):
                return 403, {"error": "forbidden"}
            return 200, prof["config"]
        if method == "GET" and path.startswith("/v1/m/sessions/provider-profiles/"):
            ref = path.rsplit("/", 1)[-1]
            prof = self.profiles.get(ref)
            if prof is None:
                return 404, {"error": "not found"}
            return 200, prof.get("reread") or prof["item"]
        if method == "POST" and path == "/v1/m/sessions/runs":
            self.run_profile_refs.append(body["provider_profile_ref"])
            return 201, {"run_ref": "run_test"}
        if method == "GET" and path == "/v1/m/sessions/runs/run_test":
            return 200, {"state": "running", "claude_session_id": "sess-coder-7a3f",
                         "live_ref": "live_test"}
        if method == "GET" and path.startswith("/v1/m/sessions/runs?live_ref="):
            return 200, {"items": [{"run_ref": "run_test"}]}
        if method == "GET" and path == "/v1/m/sessions/live/by-id/live_test":
            return 200, {"session_ref": "sess-coder-7a3f"}
        raise AssertionError("unexpected request: %s %s body=%r" % (method, path, body))


def item(ref: str, label: str, **over) -> dict:
    base = {"profile_ref": ref, "display_name": label, "driver": "claude",
            "environment_ref": "xenv_local", "state": "active", "local_environment": True,
            "operable": True}
    base.update(over)
    return base


def config(ref: str, config_home: str, user_home: str, **over) -> dict:
    base = {"profile_ref": ref, "driver": "claude", "environment_ref": "xenv_local",
            "config_home": config_home, "user_home": user_home, "state": "active"}
    base.update(over)
    return base


class SeederIdentity(unittest.TestCase):
    def setUp(self):
        self.module = load_seeder()
        self.tmp = tempfile.TemporaryDirectory(prefix="olivares-seeder-identity-")
        root = Path(self.tmp.name)
        for name in ("deploy", "src"):
            (root / name).mkdir()
        (root / "README.md").write_text("fixture\n", encoding="utf-8")
        self.root = str(root.resolve())
        self.config_home = os.path.join(self.root, ".olivares-provider-home", "config")
        self.user_home = os.path.join(self.root, ".olivares-provider-home", "user")

    def tearDown(self):
        self.tmp.cleanup()

    def run_seed(self, fake: FakeAPI):
        self.module.pedir = fake
        return self.module.sembrar_workspace_y_run("http://local.invalid", "synthetic",
                                                   "tenant-test", self.root)

    def test_same_label_twice_with_other_homes_denies_before_any_run(self):
        # The reviewer's discriminator: two profiles carry the demo label; neither owns the
        # demo homes. The label is not an identity — nothing is launched.
        fake = FakeAPI(self.root,
                       [[item("ppf_a", "acme-platform Claude home"),
                         item("ppf_b", "acme-platform Claude home")]],
                       {"ppf_a": {"item": item("ppf_a", "acme-platform Claude home"),
                                  "config": config("ppf_a", "/srv/other/a", "/srv/other/ua")},
                        "ppf_b": {"item": item("ppf_b", "acme-platform Claude home"),
                                  "config": config("ppf_b", "/srv/other/b", "/srv/other/ub")}})
        rc = self.run_seed(fake)
        self.assertEqual((rc, fake.run_profile_refs), (1, []))
        self.assertEqual(sorted(fake.config_paths), ["ppf_a", "ppf_b"])

    def test_same_label_but_only_one_exact_home_launches_the_exact_one(self):
        # Two profiles share the label; only the FIRST owns the demo homes. b9ff picked the
        # last label match; identity picks the exact one regardless of order.
        fake = FakeAPI(self.root,
                       [[item("ppf_exact", "acme-platform Claude home"),
                         item("ppf_decoy", "acme-platform Claude home")]],
                       {"ppf_exact": {"item": item("ppf_exact", "acme-platform Claude home"),
                                      "config": config("ppf_exact", self.config_home, self.user_home)},
                        "ppf_decoy": {"item": item("ppf_decoy", "acme-platform Claude home"),
                                      "config": config("ppf_decoy", "/srv/other/d", self.user_home)}})
        rc = self.run_seed(fake)
        self.assertEqual((rc, fake.run_profile_refs), (0, ["ppf_exact"]))

    def test_renamed_exact_profile_on_page_two_is_found_by_walking_the_cursor(self):
        fake = FakeAPI(self.root,
                       [[], [item("ppf_exact", "renamed by an operator")]],
                       {"ppf_exact": {"item": item("ppf_exact", "renamed by an operator"),
                                      "config": config("ppf_exact", self.config_home, self.user_home)}})
        rc = self.run_seed(fake)
        self.assertEqual((rc, fake.run_profile_refs), (0, ["ppf_exact"]))
        self.assertEqual(fake.list_paths, [
            "/v1/m/sessions/provider-profiles?state=active&limit=100",
            "/v1/m/sessions/provider-profiles?state=active&limit=100&cursor=page-2",
        ])

    def test_two_exact_identities_are_an_ambiguity_and_deny(self):
        fake = FakeAPI(self.root,
                       [[item("ppf_one", "one"), item("ppf_two", "two")]],
                       {"ppf_one": {"item": item("ppf_one", "one"),
                                    "config": config("ppf_one", self.config_home, self.user_home)},
                        "ppf_two": {"item": item("ppf_two", "two"),
                                    "config": config("ppf_two", self.config_home, self.user_home)}})
        rc = self.run_seed(fake)
        self.assertEqual((rc, fake.run_profile_refs), (1, []))

    def test_unauthorized_configuration_read_cannot_confirm_an_identity(self):
        fake = FakeAPI(self.root,
                       [[item("ppf_exact", "acme-platform Claude home")]],
                       {"ppf_exact": {"item": item("ppf_exact", "acme-platform Claude home"),
                                      "config": config("ppf_exact", self.config_home, self.user_home),
                                      "config_authorized": False}})
        rc = self.run_seed(fake)
        self.assertEqual((rc, fake.run_profile_refs), (1, []))

    def test_exact_identity_that_is_no_longer_launchable_on_reread_denies(self):
        fake = FakeAPI(self.root,
                       [[item("ppf_exact", "acme-platform Claude home")]],
                       {"ppf_exact": {"item": item("ppf_exact", "acme-platform Claude home"),
                                      "config": config("ppf_exact", self.config_home, self.user_home),
                                      "reread": item("ppf_exact", "acme-platform Claude home",
                                                     state="disabled", operable=False)}})
        rc = self.run_seed(fake)
        self.assertEqual((rc, fake.run_profile_refs), (1, []))

    def test_foreign_environment_on_detail_is_not_local_and_denies(self):
        fake = FakeAPI(self.root,
                       [[item("ppf_far", "acme-platform Claude home")]],
                       {"ppf_far": {"item": item("ppf_far", "acme-platform Claude home",
                                                 environment_ref="xenv_other",
                                                 local_environment=False, operable=False),
                                    "config": config("ppf_far", self.config_home, self.user_home,
                                                     environment_ref="xenv_other")}})
        rc = self.run_seed(fake)
        self.assertEqual((rc, fake.run_profile_refs), (1, []))

    def test_a_fresh_create_still_launches_and_rereads(self):
        fake = FakeAPI(self.root, [[]],
                       {"ppf_created": {"item": item("ppf_created", "acme-platform Claude home"),
                                        "config": config("ppf_created", self.config_home, self.user_home)}},
                       create_status=201)
        rc = self.run_seed(fake)
        self.assertEqual((rc, fake.run_profile_refs, fake.list_paths), (0, ["ppf_created"], []))


if __name__ == "__main__":
    result = unittest.main(verbosity=2, exit=False).result
    sys.exit(0 if result.wasSuccessful() else 1)
