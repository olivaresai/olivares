#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""The full mainline-ci run must not starve itself (dev#2367).

Measured on run 38901745263: `web` hit its 75-minute ceiling while the race partitions held
the eight runners, so functional-shard, race-rest and race-hot-manifest were SKIPPED and the
functional suite never started. Run 38860445399 lost it the other way: the permission check in
`web` failed and the build behind it was skipped. Two schedule facts caused it:

  1. The functional suite consumes only the built console, but the console bundle was uploaded
     by the LAST step of `web`, behind the permission check and the vitest run. The producer of
     the bundle must therefore run nothing but the build, and wait for nothing but classify.
  2. The race legs (the jobs `race-hot` aggregates) started with nothing in front of them, so
     they filled the runners while `web` and the functional shards waited. They now start after
     functional-shard and the job that runs vitest, and `!cancelled()` keeps a red upstream job
     from skipping them (a skipped leg is a red `race-hot`, and its message would blame an edit).
  3. Starting together is still too many. Once functional-shard and `web` finish, the three
     matrix families with the hours-long legs (race-modules, race-sessions, race-core) would
     become runnable with race-rest and race-hot-manifest: 17 jobs on eight runners, the same
     oversubscription moved later (Root's recipe on the issue, runners/OPTIMIZATION-PLAN-20261016
     section 2.1). Each of the three therefore runs two legs at a time, and they run one family
     after the other, so at most 2 + race-hot-manifest's 4 + race-rest's 1 = 7 race jobs run at once.

The producer, the web-test job and the artifact name are read from the parsed workflow. The
consumer set and the race-leg set are pinned here on purpose: a check that derives its subject
from the thing it checks shrinks when that thing shrinks.

This is a post-merge detector like test-ci-evidence.py: `lint:actions` runs in mainline-ci's
control-plane job, not in the pre-push hook.

The same file holds two facts about a runner host that several jobs share, for every workflow
in .github/workflows:

  4. A step that sets GOCACHE and GOMODCACHE keeps the cache the runner's environment names in
     them, and falls back to a job-private directory under RUNNER_TEMP where they are unset
     (hosted runners). A step whose script says "cold on purpose" ignores the host's caches:
     what it builds must not read a cache another job on the host can write. The `echo` lines
     are executed here, not grepped, with and without the variables. GOPATH stays job-private:
     `go install` writes binaries there. `actions/setup-go` takes its own cache paths from
     `go env`, so in a job with such a step it must not keep its default `cache: true`: it would
     compress the host's caches into a GitHub cache entry and extract one back over them. Its
     `cache:` is `false` or hosted-only (self-hosted runners keep the Go caches on local disk).
     Only a job that has such a step is judged here: a pool job that sets no cache but keeps
     the default `cache: true` inherits the host's directories once the runner sets them.
  5. Every PostgreSQL service container is probed once every 30 s after startup, not every 5 s:
     the probe is a `docker exec`, and dozens of services multiplied it. `--health-start-period`
     and `--health-start-interval` keep the readiness poll at 1 s while the server starts. The
     Actions runner waits for as long as a service reports "starting", so a slow start only costs
     up to 30 s of polling granularity; a container that never becomes healthy is reported after
     the start period plus `--health-retries` probes.
"""

from pathlib import Path
import copy
import os
import re
import subprocess
import tempfile
import unittest

import yaml

ROOT = Path(__file__).resolve().parent.parent
WORKFLOW = ROOT / '.github/workflows/mainline-ci.yml'
WORKFLOWS = sorted((ROOT / '.github/workflows').glob('*.yml'))
BUNDLE_REF = re.compile(r'needs\.([a-z0-9-]+)\.outputs\.bundle-artifact')
CONSUMERS = {'control-plane', 'functional-shard', 'race-rest', 'race-hot-manifest'}
RACE_LEGS = {'race-modules', 'race-rest', 'race-core', 'race-sessions', 'race-hot-manifest'}
# What `web` runs and the bundle producer must not: any of these in a `run:` puts the console
# build behind a web check.
WEB_CHECKS = ('vitest', 'lint:console-perms', 'test:web', 'run test')
# Root's recipe for dev#2367: `max-parallel: 2` on the three big families, chained in this order.
CHAIN = ['race-modules', 'race-sessions', 'race-core']
MAX_PARALLEL = 2
# The dev runners (ci-runner-1 hosts eight of them); the race jobs that run together must fit.
RUNNERS = 8
LEG_IF = "${{ !cancelled() && needs.classify.outputs.code == 'true' }}"
# The caches a host would point GOCACHE and GOMODCACHE at, to prove the steps keep them (and ignore a GOPATH).
HOST_CACHES = {'GOCACHE': '/host/shared/gocache', 'GOMODCACHE': '/host/shared/gomodcache'}
COLD_MARK = 'cold on purpose'
# The two values of `setup-go`'s `cache:` that never hand a host cache directory to actions/cache.
HOSTED_ONLY_CACHE = "${{ runner.environment == 'github-hosted' }}"
# What each PostgreSQL service must say about its health probe, exactly once.
HEALTH_FLAGS = {'--health-interval': '30s', '--health-start-period': '60s', '--health-start-interval': '1s'}
# The only form of a step that exports a Go path; anything else that sets one is not judged by running it.
GO_PATH_ECHO = re.compile(r'^\s*echo "(GOCACHE|GOMODCACHE|GOPATH)=[^"\n]*"\s*$', re.M)


def needs(job):
    value = job.get('needs') or []
    return [value] if isinstance(value, str) else list(value)


def runs(job, text):
    return any(text in str(step.get('run', '')) for step in job.get('steps', []))


def closure(jobs, name):
    """Every job `name` waits for, directly or through another job."""
    seen, pending = set(), needs(jobs[name])
    while pending:
        upstream = pending.pop()
        if upstream in seen or upstream not in jobs:
            continue
        seen.add(upstream)
        pending.extend(needs(jobs[upstream]))
    return seen


def findings(workflow):
    jobs = workflow['jobs']
    found = []
    web_tests = [name for name, job in jobs.items() if runs(job, 'vitest')]
    if not web_tests:
        return ['no job runs the web unit tests; the schedule cannot be judged']
    check_jobs = {name for name, job in jobs.items() if any(runs(job, m) for m in WEB_CHECKS)}
    for name in web_tests:
        if not runs(jobs[name], 'lint:console-perms'):
            found.append(f'{name} no longer runs the console permission checks')

    # 1. Every consumer of the console waits for the job that publishes it, and that job runs no
    # web check, waits for classify only, and exports the artifact name.
    downloads = {}
    for name, job in jobs.items():
        for step in job.get('steps', []):
            ref = BUNDLE_REF.search(str((step.get('with') or {}).get('name', '')))
            if ref:
                downloads[name] = ref.group(1)
    for name in sorted(CONSUMERS - set(downloads)):
        found.append(f'{name} does not download the console from a needs.<job>.outputs.bundle-artifact')
    for name, producer in downloads.items():
        if producer not in jobs:
            found.append(f'{name} downloads the console from {producer}, which is not a job')
            continue
        if producer not in needs(jobs[name]):
            found.append(f'{name} downloads the console from {producer} without needing it')
        if name not in CONSUMERS:
            found.append(f'{name} is a new consumer of the console; add it to CONSUMERS here')
    for producer in sorted(set(downloads.values()) & set(jobs)):
        job = jobs[producer]
        if producer in check_jobs:
            found.append(f'{producer} publishes the console but runs a web check ({", ".join(WEB_CHECKS)})')
        if closure(jobs, producer) - {'classify'}:
            found.append(f'{producer} publishes the console but waits for '
                         f'{sorted(closure(jobs, producer) - {"classify"})}')
        if not runs(job, 'capture-web-generation.py'):
            found.append(f'{producer} does not run capture-web-generation.py')
        if not any(str(s.get('uses', '')).startswith('actions/upload-artifact@') for s in job.get('steps', [])):
            found.append(f'{producer} publishes bundle-artifact but uploads nothing')
        if 'bundle-artifact' not in (job.get('outputs') or {}):
            found.append(f'{producer} exports no bundle-artifact output')

    # 2. The functional suite does not wait, even through another job, for a web check.
    behind = closure(jobs, 'functional-shard') & check_jobs
    if behind:
        found.append(f'functional-shard waits for {sorted(behind)}, which run web checks')

    # 3. The race legs start after the functional suite and the web unit tests, and run when an
    # upstream job failed. The aggregate still lists every leg and still runs on always().
    hot = jobs['race-hot']
    for leg in sorted(RACE_LEGS - set(needs(hot))):
        found.append(f'race-hot does not aggregate {leg}')
    if 'always()' not in str(hot.get('if', '')):
        found.append('race-hot must run on always(), or a red leg skips its verdict')
    for leg in sorted(RACE_LEGS):
        job = jobs[leg]
        for upstream in ['functional-shard', *web_tests]:
            if upstream not in closure(jobs, leg):
                found.append(f'{leg} does not wait for {upstream}')
        if job.get('if') != LEG_IF:
            found.append(f'{leg} must run on {LEG_IF}, got: {job.get("if")!r}')

    # 3b. The big families run two legs at a time and one family after the other. Each link is a
    # direct `needs:` (the leg keeps `!cancelled()`, so a red family does not skip the next one),
    # and `fail-fast: false` stays, because a cancelled leg reports nothing.
    for leg in CHAIN:
        strategy = jobs[leg].get('strategy') or {}
        if strategy.get('max-parallel') != MAX_PARALLEL:
            found.append(f'{leg} must set strategy.max-parallel: {MAX_PARALLEL}, '
                         f'got: {strategy.get("max-parallel")!r}')
        if strategy.get('fail-fast') is not False:
            found.append(f'{leg} must keep strategy.fail-fast: false')
    for before, after in zip(CHAIN, CHAIN[1:]):
        if before not in needs(jobs[after]):
            found.append(f'{after} must wait for {before}, or both families start together')

    # Peak: one chained family at its cap, plus every other race leg that is not chained. The
    # 7 = 2 + 4 + 1 of the docstring is derived from the file, so widening race-hot-manifest or
    # adding a matrix to race-rest cannot bring the oversubscription back unseen.
    def legs(job, cap=None):
        matrix = (job.get('strategy') or {}).get('matrix') or {}
        size = len(matrix['partition']) if 'partition' in matrix else 1
        return min(size, cap) if cap else size

    peak = (max(legs(jobs[leg], MAX_PARALLEL) for leg in CHAIN) +
            sum(legs(jobs[leg]) for leg in sorted(RACE_LEGS - set(CHAIN))))
    if peak > RUNNERS:
        found.append(f'{peak} race jobs can run at once on {RUNNERS} runners')

    # 4. control-plane, the required context, still carries both verdicts.
    plane = jobs['control-plane']
    if 'always()' not in str(plane.get('if', '')):
        found.append('control-plane must run on always()')
    for upstream in ['functional-shard', 'web', *set(downloads.values())]:
        if upstream not in needs(plane):
            found.append(f'control-plane does not wait for {upstream}')

    # 5. The order has no cycle (GitHub rejects the file; the check names the loop).
    state = {}

    def visit(name, path):
        if state.get(name) == 'done':
            return
        if state.get(name) == 'open':
            found.append('needs cycle: ' + ' -> '.join(path + [name]))
            return
        state[name] = 'open'
        for upstream in needs(jobs.get(name, {})):
            visit(upstream, path + [name])
        state[name] = 'done'

    for name in jobs:
        visit(name, [])
    return found


def cache_steps(workflow):
    """(job, echoes, cold, unjudgeable) of every step that assigns GOCACHE or GOMODCACHE in any form.

    echoes = the strict `echo "NAME=..."` lines of the step, which are the only part that is run;
    unjudgeable = a reason the test cannot vouch for the step (an assignment it does not recognise,
    or no redirect into $GITHUB_ENV, which is what makes the echoes reach the next steps).
    """
    found = []
    for name, job in workflow['jobs'].items():
        for step in job.get('steps', []):
            run = str(step.get('run', ''))
            if not re.search(r'\bGO(MOD)?CACHE\s*=', run):
                continue
            echoes = [m.group(0) for m in GO_PATH_ECHO.finditer(run)]
            why = None
            if not any(e.lstrip().startswith('echo "GOCACHE=') for e in echoes):
                why = 'sets a Go cache in a form this test does not recognise; use one `echo "GOCACHE=..."` line per variable'
            elif '>> "$GITHUB_ENV"' not in run:
                why = 'writes its Go caches but never redirects them into "$GITHUB_ENV"'
            found.append((name, '\n'.join(echoes), COLD_MARK in run, why))
    return found


def exported_env(echoes, inherited):
    """Run the echo lines of one step; return what they print as a dict, and the RUNNER_TEMP used."""
    with tempfile.TemporaryDirectory() as tmp:
        env = {'PATH': os.environ['PATH'], 'RUNNER_TEMP': f'{tmp}/runner', **inherited}
        out = subprocess.run(['bash', '-e', '-c', echoes], env=env, cwd=tmp, check=True,
                             capture_output=True, text=True).stdout
        return dict(line.split('=', 1) for line in out.splitlines() if '=' in line), env['RUNNER_TEMP']


def host_state_findings(workflow, require_subjects=False):
    found = []
    steps = cache_steps(workflow)
    if require_subjects and not steps:
        found.append('no step sets GOCACHE; the cache facts cannot be judged')
    for label, echoes, cold, why in steps:
        if why:
            found.append(f'{label} {why}')
            continue
        unset, temp = exported_env(echoes, {})
        if unset.get('GOCACHE') != f'{temp}/gocache' or unset.get('GOMODCACHE') != f'{temp}/gomodcache':
            found.append(f'{label} must fall back to RUNNER_TEMP when GOCACHE and GOMODCACHE are unset, '
                         f'got {unset.get("GOCACHE")!r} and {unset.get("GOMODCACHE")!r}')
        host, host_temp = exported_env(echoes, {**HOST_CACHES, 'GOPATH': '/host/shared/gopath'})
        for key, path in HOST_CACHES.items():
            if cold and host.get(key) != f'{host_temp}/{key.lower()}':
                found.append(f'{label} is marked "{COLD_MARK}" but takes the host {key}, got {host.get(key)!r}')
            elif not cold and host.get(key) != path:
                found.append(f'{label} must keep the host {key} when the runner sets it, got {host.get(key)!r}')
        if 'GOPATH' in host and host['GOPATH'] != f'{host_temp}/gopath':
            found.append(f'{label} must keep GOPATH under RUNNER_TEMP, got {host["GOPATH"]!r}')
    for name in sorted({job for job, _, cold, _ in steps if not cold}):
        for step in workflow['jobs'][name].get('steps', []):
            if not str(step.get('uses', '')).startswith('actions/setup-go@'):
                continue
            cache = (step.get('with') or {}).get('cache')
            if cache is not False and cache != HOSTED_ONLY_CACHE:
                found.append(f'{name} keeps the host Go caches, so its setup-go must set cache: false or '
                             f'{HOSTED_ONLY_CACHE}, got {cache!r}')
    services = [(f'{name}.{svc_name}', (svc.get('options') or '').split())
                for name, job in workflow['jobs'].items()
                for svc_name, svc in (job.get('services') or {}).items()
                if re.search(r'postgres|pgvector', str(svc.get('image', '')), re.I)]
    if require_subjects and not services:
        found.append('no job has a PostgreSQL service container; the health facts cannot be judged')
    for label, tokens in services:
        if '--health-cmd' not in tokens:
            found.append(f'{label} must set --health-cmd, or it is never probed')
        for flag, value in HEALTH_FLAGS.items():
            got = [tokens[i + 1] for i, t in enumerate(tokens) if t == flag and i + 1 < len(tokens)]
            if got != [value]:
                found.append(f'{label} must set {flag} {value} once, got {got or "nothing"}')
    return found


class MainlineSchedule(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.workflow = yaml.safe_load(WORKFLOW.read_text())

    def mutant(self, change):
        workflow = copy.deepcopy(self.workflow)
        change(workflow['jobs'])
        return findings(workflow)

    def producer(self, jobs):
        return next(n for n, j in jobs.items()
                    if any(str(s.get('uses', '')).startswith('actions/upload-artifact@')
                           and 'web-generation' in str((s.get('with') or {}).get('name', ''))
                           for s in j['steps']))

    def assertCaught(self, got, text):
        self.assertTrue(any(text in f for f in got), got)

    def test_real_workflow_does_not_starve_itself(self):
        self.assertEqual(findings(self.workflow), [])

    def test_functional_suite_waiting_for_vitest_is_caught(self):
        got = self.mutant(lambda jobs: jobs['functional-shard'].update(needs=['classify', 'web']))
        self.assertCaught(got, 'functional-shard waits for')

    def test_functional_suite_waiting_through_another_job_is_caught(self):
        def change(jobs):
            jobs['helper'] = {'needs': ['classify', 'web'], 'steps': []}
            jobs['functional-shard']['needs'] = ['classify', 'web-bundle', 'helper']
        self.assertCaught(self.mutant(change), 'functional-shard waits for')

    def test_console_published_behind_vitest_is_caught(self):
        def change(jobs):
            jobs[self.producer(jobs)]['steps'].insert(0, {'run': 'pnpm --dir web exec vitest run'})
        self.assertCaught(self.mutant(change), 'runs a web check')

    def test_console_published_behind_another_web_check_is_caught(self):
        def change(jobs):
            jobs[self.producer(jobs)]['steps'].insert(0, {'run': 'task test:web'})
        self.assertCaught(self.mutant(change), 'runs a web check')

    def test_console_published_after_web_is_caught(self):
        def change(jobs):
            jobs[self.producer(jobs)]['needs'] = ['classify', 'web']
        self.assertCaught(self.mutant(change), 'publishes the console but waits for')

    def test_web_losing_its_permission_checks_is_caught(self):
        def change(jobs):
            for step in jobs['web']['steps']:
                step['run'] = str(step.get('run', '')).replace('lint:console-perms', 'true')
        self.assertCaught(self.mutant(change), 'no longer runs the console permission checks')

    def test_consumer_not_waiting_for_the_producer_is_caught(self):
        got = self.mutant(lambda jobs: jobs['race-rest'].update(
            needs=['classify', 'functional-shard', 'web']))
        self.assertCaught(got, 'race-rest downloads the console')

    def test_consumer_that_stops_downloading_is_caught(self):
        def change(jobs):
            jobs['race-hot-manifest']['steps'] = [
                s for s in jobs['race-hot-manifest']['steps']
                if 'download-artifact' not in str(s.get('uses', ''))]
        self.assertCaught(self.mutant(change), 'race-hot-manifest does not download the console')

    def test_producer_without_its_output_is_caught(self):
        def change(jobs):
            jobs[self.producer(jobs)]['outputs'] = {}
        self.assertCaught(self.mutant(change), 'exports no bundle-artifact output')

    def test_race_leg_starting_early_is_caught(self):
        got = self.mutant(lambda jobs: jobs['race-core'].update(needs=['classify']))
        self.assertCaught(got, 'race-core does not wait for functional-shard')
        self.assertCaught(got, 'race-core does not wait for web')

    def test_race_leg_skipped_by_a_red_upstream_is_caught(self):
        got = self.mutant(lambda jobs: jobs['race-sessions'].update(
            {'if': "needs.classify.outputs.code == 'true'"}))
        self.assertCaught(got, 'race-sessions must run on')

    def test_race_leg_running_on_a_cancelled_run_is_caught(self):
        got = self.mutant(lambda jobs: jobs['race-modules'].update(
            {'if': "${{ !cancelled() || needs.classify.outputs.code == 'true' }}"}))
        self.assertCaught(got, 'race-modules must run on')

    def test_big_family_without_the_cap_is_caught(self):
        for leg in CHAIN:
            def change(jobs, leg=leg):
                jobs[leg]['strategy'].pop('max-parallel', None)
            self.assertCaught(self.mutant(change), f'{leg} must set strategy.max-parallel')

    def test_big_family_with_a_wider_cap_is_caught(self):
        for leg in CHAIN:
            def change(jobs, leg=leg):
                jobs[leg]['strategy']['max-parallel'] = 4
            self.assertCaught(self.mutant(change), f'{leg} must set strategy.max-parallel: 2')

    def test_big_family_that_cancels_its_siblings_is_caught(self):
        for leg in CHAIN:
            def change(jobs, leg=leg):
                jobs[leg]['strategy']['fail-fast'] = True
            self.assertCaught(self.mutant(change), f'{leg} must keep strategy.fail-fast: false')

    def test_uncapped_leg_that_grows_past_the_runners_is_caught(self):
        for leg in sorted(RACE_LEGS - set(CHAIN)):
            def change(jobs, leg=leg):
                jobs[leg].setdefault('strategy', {}).setdefault('matrix', {})['partition'] = list(range(1, 9))
            self.assertCaught(self.mutant(change), 'race jobs can run at once')

    def test_families_starting_together_are_caught(self):
        for before, after in zip(CHAIN, CHAIN[1:]):
            def change(jobs, before=before, after=after):
                jobs[after]['needs'] = [n for n in jobs[after]['needs'] if n != before]
            self.assertCaught(self.mutant(change), f'{after} must wait for {before}')

    def test_aggregate_losing_a_leg_is_caught(self):
        for leg in sorted(RACE_LEGS):
            def change(jobs, leg=leg):
                jobs['race-hot']['needs'] = [n for n in jobs['race-hot']['needs'] if n != leg]
            self.assertCaught(self.mutant(change), f'race-hot does not aggregate {leg}')

    def test_aggregate_skipped_by_a_red_leg_is_caught(self):
        got = self.mutant(lambda jobs: jobs['race-hot'].update({'if': "needs.classify.outputs.code == 'true'"}))
        self.assertCaught(got, 'race-hot must run on always()')

    def test_control_plane_losing_a_verdict_is_caught(self):
        got = self.mutant(lambda jobs: jobs['control-plane'].update(
            needs=['classify', 'functional-shard', 'web-bundle']))
        self.assertCaught(got, 'control-plane does not wait for web')

    def test_cycle_is_caught(self):
        got = self.mutant(lambda jobs: jobs['classify'].update(needs=['functional-shard']))
        self.assertCaught(got, 'needs cycle')


class MainlineHostState(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.workflow = yaml.safe_load(WORKFLOW.read_text())

    def mutant(self, change):
        workflow = copy.deepcopy(self.workflow)
        change(workflow['jobs'])
        self.assertNotEqual(workflow, self.workflow, 'the mutation changed nothing')
        return host_state_findings(workflow, require_subjects=True)

    def assertCaught(self, got, text):
        self.assertTrue(any(text in f for f in got), got)

    def cache_step(self, jobs, job):
        return next(s for s in jobs[job]['steps'] if 'GOCACHE=' in str(s.get('run', '')))

    def test_every_workflow_shares_the_host_and_probes_slowly(self):
        self.assertEqual(host_state_findings(self.workflow, require_subjects=True), [])
        for path in WORKFLOWS:
            with self.subTest(workflow=path.name):
                self.assertEqual(host_state_findings(yaml.safe_load(path.read_text())), [])

    def test_cache_step_that_ignores_the_host_cache_is_caught(self):
        def change(jobs):
            step = self.cache_step(jobs, 'race-core')
            step['run'] = step['run'].replace('${GOCACHE:-$RUNNER_TEMP/gocache}', '$RUNNER_TEMP/gocache')
        self.assertCaught(self.mutant(change), 'race-core must keep the host GOCACHE')

    def test_cache_step_that_ignores_the_host_module_cache_is_caught(self):
        def change(jobs):
            step = self.cache_step(jobs, 'control-plane')
            step['run'] = step['run'].replace('${GOMODCACHE:-$RUNNER_TEMP/gomodcache}', '$RUNNER_TEMP/gomodcache')
        self.assertCaught(self.mutant(change), 'control-plane must keep the host GOMODCACHE')

    def test_cache_step_without_the_hosted_fallback_is_caught(self):
        def change(jobs):
            step = self.cache_step(jobs, 'race-rest')
            step['run'] = step['run'].replace('${GOCACHE:-$RUNNER_TEMP/gocache}', '${GOCACHE}')
        self.assertCaught(self.mutant(change), 'must fall back to RUNNER_TEMP')

    def test_shared_gopath_is_caught(self):
        def change(jobs):
            step = self.cache_step(jobs, 'race-core')
            step['run'] = step['run'].replace('"GOPATH=$RUNNER_TEMP/gopath"', '"GOPATH=${GOPATH:-$RUNNER_TEMP/gopath}"')
        self.assertCaught(self.mutant(change), 'must keep GOPATH under RUNNER_TEMP')

    def test_cache_step_in_a_form_the_test_cannot_read_is_caught(self):
        def change(jobs):
            step = self.cache_step(jobs, 'race-core')
            step['run'] = 'echo "GOCACHE=/tmp/x" >> "$GITHUB_ENV"\n'
        self.assertCaught(self.mutant(change), 'race-core sets a Go cache in a form this test does not recognise')

    def test_cache_step_that_never_reaches_github_env_is_caught(self):
        def change(jobs):
            step = self.cache_step(jobs, 'race-core')
            step['run'] = step['run'].replace('>> "$GITHUB_ENV"', '> /dev/null')
        self.assertCaught(self.mutant(change), 'race-core writes its Go caches but never redirects them')

    def test_cold_step_that_reads_the_host_cache_is_caught(self):
        def change(jobs):
            step = self.cache_step(jobs, 'race-core')
            step['run'] = f'# {COLD_MARK}\n' + step['run']
        self.assertCaught(self.mutant(change), f'race-core is marked "{COLD_MARK}" but takes the host GOCACHE')

    def test_cold_step_that_stays_cold_is_clean(self):
        workflow = {'jobs': {'release': {'steps': [{'run': f'# {COLD_MARK}\n{{\n  echo "GOCACHE=$RUNNER_TEMP/gocache"\n'
                                                           '  echo "GOMODCACHE=$RUNNER_TEMP/gomodcache"\n} >> "$GITHUB_ENV"'}]}}}
        self.assertEqual(host_state_findings(workflow, require_subjects=False), [])

    def setup_go(self, jobs, job):
        return next(s for s in jobs[job]['steps'] if str(s.get('uses', '')).startswith('actions/setup-go@'))

    def test_setup_go_that_could_cache_the_host_directories_is_caught(self):
        other = "${{ runner.environment != 'github-hosted' }}"
        for job, cache in (('web', None), ('secrets', None), ('race-core', True), ('race-core', other)):
            def change(jobs, job=job, cache=cache):
                with_ = self.setup_go(jobs, job).setdefault('with', {})
                if cache is None:
                    del with_['cache']
                else:
                    with_['cache'] = cache
            with self.subTest(job=job, cache=cache):
                self.assertCaught(self.mutant(change), f'{job} keeps the host Go caches, so its setup-go must set cache')

    def test_setup_go_without_a_with_block_next_to_a_host_cache_is_caught(self):
        workflow = {'jobs': {'job': {'steps': [
            {'run': '{\n  echo "GOCACHE=${GOCACHE:-$RUNNER_TEMP/gocache}"\n'
                    '  echo "GOMODCACHE=${GOMODCACHE:-$RUNNER_TEMP/gomodcache}"\n} >> "$GITHUB_ENV"'},
            {'uses': 'actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e'}]}}}
        self.assertCaught(host_state_findings(workflow), 'job keeps the host Go caches, so its setup-go must set cache')

    def test_setup_go_that_never_caches_is_clean(self):
        def change(jobs):
            self.setup_go(jobs, 'race-core')['with']['cache'] = False
        self.assertEqual(self.mutant(change), [])

    def test_setup_go_in_a_job_with_only_a_cold_step_is_not_judged(self):
        workflow = {'jobs': {'release': {'steps': [
            {'run': f'# {COLD_MARK}\n{{\n  echo "GOCACHE=$RUNNER_TEMP/gocache"\n'
                    '  echo "GOMODCACHE=$RUNNER_TEMP/gomodcache"\n} >> "$GITHUB_ENV"'},
            {'uses': 'actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e', 'with': {'go-version-file': 'go.work'}}]}}}
        self.assertEqual(host_state_findings(workflow, require_subjects=False), [])

    def test_service_probed_every_five_seconds_is_caught(self):
        for job, name in (('control-plane', 'postgres'), ('race-sessions', 'postgres_other')):
            def change(jobs, job=job, name=name):
                svc = jobs[job]['services'][name]
                svc['options'] = svc['options'].replace('--health-interval 30s', '--health-interval 5s')
            self.assertCaught(self.mutant(change), f'{job}.{name} must set --health-interval 30s')

    def test_service_without_the_start_poll_is_caught(self):
        for flag in ('--health-start-period 60s', '--health-start-interval 1s'):
            def change(jobs, flag=flag):
                svc = jobs['race-rest']['services']['postgres']
                svc['options'] = svc['options'].replace(flag, '')
            self.assertCaught(self.mutant(change), f'race-rest.postgres must set {flag}')

    def test_service_with_a_second_interval_is_caught(self):
        def change(jobs):
            jobs['race-rest']['services']['postgres']['options'] += ' --health-interval 5s'
        self.assertCaught(self.mutant(change), 'race-rest.postgres must set --health-interval 30s once')

    def test_service_never_probed_is_caught(self):
        def change(jobs):
            svc = jobs['race-rest']['services']['postgres']
            svc['options'] = svc['options'].replace('--health-cmd pg_isready', '')
        self.assertCaught(self.mutant(change), 'race-rest.postgres must set --health-cmd')

    def test_workflow_without_cache_steps_or_services_is_not_judged_clean(self):
        got = self.mutant(lambda jobs: [jobs.pop(name) for name in list(jobs)])
        self.assertCaught(got, 'no step sets GOCACHE')
        self.assertCaught(got, 'no job has a PostgreSQL service container')


if __name__ == '__main__':
    unittest.main()
