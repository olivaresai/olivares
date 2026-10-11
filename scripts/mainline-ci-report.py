#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only

"""Reconcile completed mainline jobs with their GitHub tracking issues."""

import json
from itertools import count
import os
from pathlib import Path
import re
import urllib.error
import urllib.request


BOT = "github-actions[bot]"
FAILED = {"failure", "timed_out", "startup_failure", "action_required"}
MARKER = re.compile(r"<!-- mainline-ci run (\d+) attempt (\d+) -->")
LEGACY = re.compile(r"run (\d+) attempt (\d+)")


def pages(request, path, field=None):
    for page in count(1):
        result = request("GET", f"{path}&per_page=100&page={page}")
        items = result[field] if field else result
        yield from items
        if len(items) < 100:
            return


def reconcile_run(run, repo, request):
    if (repo != "olivaresai/olivares" or run.get("event") != "push"
            or run.get("status") != "completed" or run.get("head_branch") != "main"
            or (run.get("head_repository") or {}).get("full_name") != repo):
        return

    base = f"/repos/{repo}"
    run_id, attempt = int(run["id"]), int(run["run_attempt"])
    current = (run_id, attempt)
    marker = f"<!-- mainline-ci run {run_id} attempt {attempt} -->"
    latest_jobs = {}
    # A single-partition rerun must retain the other partitions' last verdicts.
    for job in pages(request, f"{base}/actions/runs/{run_id}/jobs?filter=all", "jobs"):
        job_attempt = job.get("run_attempt", 1)
        previous = latest_jobs.get(job["name"])
        if job_attempt <= attempt and (previous is None or job_attempt > previous.get("run_attempt", 1)):
            latest_jobs[job["name"]] = job
    groups = {}
    for job in latest_jobs.values():
        name = job["name"].split(" (", 1)[0]
        groups.setdefault(name, []).append(job)

    issues = list(pages(request, f"{base}/issues?state=all"))
    for name, jobs in groups.items():
        failed = [job for job in jobs if job["conclusion"] in FAILED]
        green = all(job["conclusion"] == "success" for job in jobs)
        if not failed and not green:
            continue
        title = f"mainline-ci: job {name} failed on main"
        matches = [issue for issue in issues if "pull_request" not in issue
                   and issue["user"]["login"] == BOT
                   and (issue["title"] == title or issue["title"].startswith(title + " @ "))]
        issue = max(matches, key=lambda item: item["number"], default=None)
        path = f"{base}/issues/{issue['number']}" if issue else None
        history = {}
        for match in matches:
            history[match["number"]] = [match.get("body") or ""]
            history[match["number"]].extend(
                comment.get("body") or "" for comment in pages(
                    request, f"{base}/issues/{match['number']}/comments?")
                if comment["user"]["login"] == BOT)
        latest = max((tuple(map(int, match)) for bodies in history.values() for body in bodies
                      for match in (MARKER.findall(body) or LEGACY.findall(body))), default=(0, 0))
        if latest > current:
            continue
        # A delivered recovery comment may have succeeded before the close failed.
        delivered = issue and any(marker in body for body in history[issue["number"]])
        url = run["html_url"]
        body = f"{marker}\nRun [{run_id}, attempt {attempt}]({url}) · commit `{run['head_sha']}`.\n"
        duplicates = [match for match in matches if match is not issue]
        if duplicates:
            body += "\nPrevious reports: " + ", ".join(f"#{match['number']}" for match in duplicates) + ".\n"
        if failed:
            if not delivered:
                body += "\nFailed jobs:\n"
                for job in failed:
                    steps = ", ".join(step["name"] for step in job.get("steps", [])
                                      if step.get("conclusion") in FAILED) or job["conclusion"]
                    body += f"\n- [{job['name']}]({job.get('html_url', url)}): {steps}."
                if issue:
                    request("POST", path + "/comments", {"body": body})
                else:
                    request("POST", base + "/issues", {"title": title, "body": body})
            if issue and issue["state"] == "closed":
                request("PATCH", path, {"state": "open"})
        elif issue and issue["state"] == "open":
            if not delivered:
                request("POST", path + "/comments", {"body": body + "\nRecovered: every job partition passed."})
            request("PATCH", path, {"state": "closed", "state_reason": "completed"})
        for duplicate in duplicates:
            if duplicate["state"] != "open":
                continue
            duplicate_path = f"{base}/issues/{duplicate['number']}"
            if not any(marker in text for text in history[duplicate["number"]]):
                request("POST", duplicate_path + "/comments", {
                    "body": f"{marker}\nTracked in #{issue['number']}; prior reports are preserved here."})
            request("PATCH", duplicate_path, {"state": "closed",
                    "state_reason": "not_planned" if failed else "completed"})


def main():
    event = json.loads(Path(os.environ["GITHUB_EVENT_PATH"]).read_text())
    token = os.environ["GH_TOKEN"]
    api = os.environ.get("GITHUB_API_URL", "https://api.github.com").rstrip("/")

    def request(method, path, data=None):
        payload = json.dumps(data).encode() if data is not None else None
        req = urllib.request.Request(api + path, data=payload, method=method, headers={
            "Authorization": f"Bearer {token}",
            "Accept": "application/vnd.github+json",
            "Content-Type": "application/json",
            "X-GitHub-Api-Version": "2022-11-28",
        })
        try:
            with urllib.request.urlopen(req, timeout=30) as response:
                return json.load(response)
        except urllib.error.HTTPError as error:
            raise RuntimeError(f"GitHub {method} {path}: HTTP {error.code}") from None

    reconcile_run(event["workflow_run"], os.environ["GITHUB_REPOSITORY"], request)


if __name__ == "__main__":
    main()
