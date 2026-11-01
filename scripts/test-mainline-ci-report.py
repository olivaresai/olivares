#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only

import copy
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location(
    "report", Path(__file__).with_name("mainline-ci-report.py")
)
report = importlib.util.module_from_spec(spec)
spec.loader.exec_module(report)


class GitHub:
    def __init__(self):
        self.jobs = [{"name": "web", "conclusion": "failure", "steps": [], "html_url": "https://example.com/job"}]
        self.issues = []
        self.comments = {}

    def request(self, method, path, data=None):
        if method == "GET":
            page = int(path.split("page=")[-1])
            start = (page - 1) * 100
            if "/jobs?" in path:
                jobs = self.jobs
                if "/attempts/" in path:
                    attempt = int(path.split("/attempts/")[1].split("/")[0])
                    jobs = [job for job in jobs if job.get("run_attempt", attempt) == attempt]
                return {"jobs": jobs[start:start + 100]}
            if "/comments?" in path:
                number = int(path.split("/issues/")[1].split("/")[0])
                return self.comments.get(number, [])[start:start + 100]
            return self.issues[start:start + 100]
        if method == "PATCH":
            issue = next(x for x in self.issues if x["number"] == int(path.split("/")[-1]))
            issue.update(data)
            return issue
        if path.endswith("/comments"):
            number = int(path.split("/issues/")[1].split("/")[0])
            self.comments.setdefault(number, []).append({"user": {"login": report.BOT}, **data})
            return data
        issue = {"number": len(self.issues) + 1, "state": "open", "user": {"login": report.BOT}, **data}
        self.issues.append(issue)
        return issue


class ReporterTest(unittest.TestCase):
    def setUp(self):
        self.github = GitHub()
        self.run = {
            "id": 100, "run_attempt": 1, "event": "push", "status": "completed",
            "head_branch": "main", "head_sha": "a" * 40,
            "head_repository": {"full_name": "olivaresai/olivares"},
            "html_url": "https://example.com/run/100",
        }

    def reconcile(self):
        report.reconcile_run(self.run, "olivaresai/olivares", self.github.request)

    def test_recurrence_comments_on_one_issue_and_green_closes_it(self):
        self.reconcile()
        self.run["id"] = 101
        self.reconcile()
        self.assertEqual(len(self.github.issues), 1)
        self.assertEqual(len(self.github.comments[1]), 1)
        self.assertIn("101", self.github.comments[1][0]["body"])
        self.run["id"] = 102
        self.github.jobs[0]["conclusion"] = "success"
        self.reconcile()
        self.assertEqual(self.github.issues[0]["state"], "closed")
        self.assertIn("Recovered", self.github.comments[1][-1]["body"])

    def test_old_title_is_reused_and_repeat_delivery_is_idempotent(self):
        self.github.issues = [{"number": 1, "state": "open", "user": {"login": report.BOT}, "title": "mainline-ci: job web failed on main @ old", "body": "Old failure"}]
        self.reconcile()
        self.reconcile()
        self.assertEqual(len(self.github.issues), 1)
        self.assertEqual(len(self.github.comments[1]), 1)

    def test_existing_duplicates_converge_on_failure_and_all_close_on_green(self):
        self.github.issues = [
            {"number": n, "state": "open", "user": {"login": report.BOT},
             "title": f"mainline-ci: job web failed on main @ old{n}", "body": "Old failure"}
            for n in [210, 213, 217]
        ]
        self.github.issues.extend([
            {"number": 218, "state": "open", "user": {"login": "contributor"},
             "title": "mainline-ci: job web failed on main", "body": "User issue"},
            {"number": 219, "state": "open", "user": {"login": report.BOT},
             "title": "mainline-ci: job examples failed on main", "body": "Other job"},
        ])
        self.reconcile()
        self.assertEqual([issue["state"] for issue in self.github.issues],
                         ["closed", "closed", "open", "open", "open"])
        for number in [210, 213]:
            self.assertIn("#217", self.github.comments[number][-1]["body"])
        before = copy.deepcopy(self.github.comments)
        self.reconcile()
        self.assertEqual(self.github.comments, before)
        self.run["id"] += 1
        self.github.jobs[0]["conclusion"] = "success"
        self.reconcile()
        self.assertEqual([issue["state"] for issue in self.github.issues],
                         ["closed", "closed", "closed", "open", "open"])

    def test_a_newer_verdict_in_an_older_issue_is_preserved(self):
        self.github.issues = [
            {"number": n, "state": "open", "user": {"login": report.BOT},
             "title": f"mainline-ci: job web failed on main @ old{n}", "body": "Old failure"}
            for n in [213, 217]
        ]
        self.github.comments[213] = [{"user": {"login": report.BOT},
                                      "body": "<!-- mainline-ci run 102 attempt 1 -->"}]
        self.github.jobs[0]["conclusion"] = "success"
        self.reconcile()
        self.assertEqual([issue["state"] for issue in self.github.issues], ["open", "open"])
        self.assertNotIn(217, self.github.comments)

    def test_first_green_closes_every_legacy_issue(self):
        self.github.issues = [
            {"number": n, "state": "open", "user": {"login": report.BOT},
             "title": f"mainline-ci: job web failed on main @ old{n}", "body": "Old failure"}
            for n in [210, 213, 217]
        ]
        self.github.jobs[0]["conclusion"] = "success"
        self.reconcile()
        self.assertTrue(all(issue["state"] == "closed" for issue in self.github.issues))
        self.assertIn("Recovered", self.github.comments[217][-1]["body"])
        for number in [210, 213]:
            self.assertIn("#217", self.github.comments[number][-1]["body"])

    def test_one_green_matrix_partition_does_not_close_a_red_job(self):
        self.github.jobs = [
            {"name": "race-core (partition 1)", "conclusion": "success", "steps": []},
            {"name": "race-core (partition 2)", "conclusion": "failure", "steps": []},
        ]
        self.reconcile()
        self.assertEqual(self.github.issues[0]["title"], "mainline-ci: job race-core failed on main")
        self.run["id"] += 1
        self.reconcile()
        self.assertEqual(self.github.issues[0]["state"], "open")

    def test_canceled_and_skipped_runs_are_not_recovery(self):
        self.reconcile()
        for conclusion in ["cancelled", "skipped", None]:
            self.run["id"] += 1
            self.github.jobs[0]["conclusion"] = conclusion
            self.reconcile()
        self.assertEqual(self.github.issues[0]["state"], "open")

    def test_an_older_green_run_cannot_close_a_newer_failure(self):
        self.reconcile()
        self.run["id"] = 99
        self.github.jobs[0]["conclusion"] = "success"
        self.reconcile()
        self.assertEqual(self.github.issues[0]["state"], "open")

    def test_higher_attempt_of_same_run_recovers(self):
        self.reconcile()
        self.run["run_attempt"] = 2
        self.github.jobs[0]["conclusion"] = "success"
        self.reconcile()
        self.assertEqual(self.github.issues[0]["state"], "closed")

    def test_existing_issue_after_first_page_is_found(self):
        self.github.issues = [{"number": n, "state": "open", "user": {"login": report.BOT}, "title": f"User issue {n}", "body": ""} for n in range(1, 101)]
        self.github.issues.append({"number": 101, "state": "open", "user": {"login": report.BOT}, "title": "mainline-ci: job web failed on main @ old", "body": ""})
        self.reconcile()
        self.assertEqual(len(self.github.issues), 101)
        self.assertEqual(len(self.github.comments[101]), 1)

    def test_a_new_failure_reopens_the_same_tracking_issue(self):
        self.reconcile()
        self.run["id"] += 1
        self.github.jobs[0]["conclusion"] = "success"
        self.reconcile()
        self.run["id"] += 1
        self.github.jobs[0]["conclusion"] = "failure"
        self.reconcile()
        self.assertEqual(len(self.github.issues), 1)
        self.assertEqual(self.github.issues[0]["state"], "open")

    def test_an_older_failure_cannot_reopen_a_newer_recovery(self):
        self.reconcile()
        self.run["id"] = 102
        self.github.jobs[0]["conclusion"] = "success"
        self.reconcile()
        self.run["id"] = 101
        self.github.jobs[0]["conclusion"] = "failure"
        self.reconcile()
        self.assertEqual(self.github.issues[0]["state"], "closed")

    def test_recovery_close_is_retried_without_a_duplicate_comment(self):
        self.reconcile()
        self.run["id"] += 1
        self.github.jobs[0]["conclusion"] = "success"
        real = self.github.request

        def fail_close(method, path, data=None):
            if method == "PATCH":
                raise RuntimeError("GitHub unavailable")
            return real(method, path, data)

        with self.assertRaises(RuntimeError):
            report.reconcile_run(self.run, "olivaresai/olivares", fail_close)
        self.reconcile()
        self.assertEqual(self.github.issues[0]["state"], "closed")
        self.assertEqual(len(self.github.comments[1]), 1)

    def test_a_user_comment_cannot_forge_a_newer_report(self):
        self.reconcile()
        self.github.comments[1] = [{"user": {"login": "contributor"}, "body": "<!-- mainline-ci run 999 attempt 1 -->"}]
        self.run["id"] += 1
        self.github.jobs[0]["conclusion"] = "success"
        self.reconcile()
        self.assertEqual(self.github.issues[0]["state"], "closed")

    def test_job_pagination_keeps_a_failed_last_partition(self):
        self.github.jobs = [{"name": f"matrix ({n})", "conclusion": "success"} for n in range(100)]
        self.github.jobs.append({"name": "matrix (last)", "conclusion": "failure"})
        self.reconcile()
        self.assertEqual(len(self.github.issues), 1)
        self.assertIn("matrix (last)", self.github.issues[0]["body"])

    def test_a_partial_rerun_keeps_other_failed_partitions(self):
        self.github.jobs = [
            {"name": "matrix (1)", "run_attempt": 1, "conclusion": "failure"},
            {"name": "matrix (2)", "run_attempt": 1, "conclusion": "failure"},
        ]
        self.reconcile()
        self.run["run_attempt"] = 2
        self.github.jobs.append({"name": "matrix (1)", "run_attempt": 2, "conclusion": "success"})
        self.reconcile()
        self.assertEqual(self.github.issues[0]["state"], "open")
        self.run["run_attempt"] = 3
        self.github.jobs.append({"name": "matrix (2)", "run_attempt": 3, "conclusion": "success"})
        self.reconcile()
        self.assertEqual(self.github.issues[0]["state"], "closed")

    def test_non_main_or_foreign_runs_cannot_write(self):
        for field, value in [("event", "pull_request"), ("status", "in_progress"), ("head_branch", "other"), ("head_repository", {"full_name": "fork/olivares"})]:
            with self.subTest(field=field):
                run = copy.deepcopy(self.run)
                run[field] = value
                report.reconcile_run(run, "olivaresai/olivares", self.github.request)
        self.assertEqual(self.github.issues, [])

    def test_the_scheduled_battery_pass_reports_from_the_repository_that_runs_it(self):
        # The Community batteries run on a schedule against main, off the merge path. The run's
        # name is its run-name, which extends the workflow name.
        self.run.update(name="merge-gate-check main batteries", event="schedule",
                        head_repository={"full_name": "owner/development"})
        self.github.jobs = [{"name": "community-functional (shard-3)", "conclusion": "failure",
                             "steps": [], "html_url": "https://example.com/job"},
                            {"name": "community-functional (shard-4)", "conclusion": "success"},
                            {"name": "check", "conclusion": "skipped"}]
        report.reconcile_run(self.run, "owner/development", self.github.request)
        self.assertEqual([issue["title"] for issue in self.github.issues],
                         ["mainline-ci: job community-functional failed on main"])

    def test_weekly_secrets_failure_and_recovery_are_reported(self):
        repo = "owner/development"
        self.run.update(name="secrets-weekly", event="schedule", head_repository={"full_name": repo})
        report.reconcile_run(self.run, repo, self.github.request)
        self.assertEqual(len(self.github.issues), 1)
        self.run["id"] += 1
        self.github.jobs[0]["conclusion"] = "success"
        report.reconcile_run(self.run, repo, self.github.request)
        self.assertEqual(self.github.issues[0]["state"], "closed")

    def scheduled_run(self, run_id, jobs):
        self.run.update(id=run_id, name="merge-gate-check main batteries", event="schedule",
                        head_repository={"full_name": "owner/development"})
        self.github.jobs = [{"name": name, "conclusion": conclusion, "steps": [], "html_url": "https://example.com/job"}
                            for name, conclusion in jobs]
        report.reconcile_run(self.run, "owner/development", self.github.request)

    def test_a_scheduled_battery_run_reports_each_matrix_group_once_and_recovers(self):
        shards = [f"community-functional (shard-{n})" for n in range(1, 17)]
        installers = [f"community-installer ({family}, {family}:latest)" for family in ("debian", "fedora", "ubuntu")]
        green = [("community-partition", "success"), ("community-contracts", "success"), ("check", "skipped"),
                 *((name, "success") for name in shards + installers)]
        red = [(name, "failure" if name == installers[1] else conclusion) for name, conclusion in green]
        self.scheduled_run(100, red)
        self.assertEqual([issue["title"] for issue in self.github.issues],
                         ["mainline-ci: job community-installer failed on main"])
        # A cancelled newer run neither closes nor re-reports it.
        self.scheduled_run(101, [(name, "cancelled") for name, _ in green])
        self.assertEqual((len(self.github.issues), self.github.issues[0]["state"]), (1, "open"))
        # A green scheduled run recovers it.
        self.scheduled_run(102, green)
        self.assertEqual((len(self.github.issues), self.github.issues[0]["state"]), (1, "closed"))

    def test_a_failed_partition_is_reported_and_its_skipped_dependents_are_not(self):
        self.scheduled_run(100, [("community-partition", "failure"),
                                 ("community-functional (shard-1)", "skipped"),
                                 ("community-installer (debian, debian:latest)", "skipped"),
                                 ("check", "skipped")])
        self.assertEqual([issue["title"] for issue in self.github.issues],
                         ["mainline-ci: job community-partition failed on main"])

    def test_only_the_scheduled_battery_pass_of_a_gate_workflow_reports(self):
        # A gate dispatch on a candidate, an unnamed workflow and mainline-ci outside the public
        # repository never write, whatever their events.
        for name, event, repo in [("merge-gate-check mg-1-abc", "workflow_dispatch", "owner/development"),
                                  ("merge-gate-check", "push", "owner/development"),
                                  ("mainline-ci", "push", "owner/development"),
                                  ("mainline-ci", "schedule", "olivaresai/olivares"),
                                  ("other", "schedule", "owner/development"),
                                  ("other", None, "owner/development")]:
            with self.subTest(name=name, event=event, repo=repo):
                run = copy.deepcopy(self.run)
                run.update(name=name, event=event, head_repository={"full_name": repo})
                report.reconcile_run(run, repo, self.github.request)
        self.assertEqual(self.github.issues, [])


if __name__ == "__main__":
    unittest.main()
