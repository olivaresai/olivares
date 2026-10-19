#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Concurrent-session load ladder on one engine binary.

Starts one engine on a throwaway data dir with a labelled stand-in tool (no vendor account here), then
raises the number of live sessions step by step and measures, at each step, what the engine and its
session processes cost and how the API answers. Stops everything at the end.

Stand-in: scripts/loadtest-sessions-standin.sh answers as a signed-in Claude Code and echoes every turn; it runs no
model, so the numbers are the engine's own cost per session and the real tool's memory and CPU come on top.
Measured per step: start and time-to-running latency, engine RSS/threads/fds/CPU, child processes, API latency with N
live sessions (list, get, /readyz), a turn sent to every session and its reply latency on the output stream, and the stop wave.
It stops short of --max-sum-rss-mib (summed RSS of the engine, its children and this script) and aborts a step when the
host's process count passes --guard-pids or MemAvailable falls under --guard-avail-mib.

usage: loadtest-sessions.py BIN WORK STEPS [--workers N] [--guard-pids N] [--guard-avail-mib N] [--max-sum-rss-mib N]
  BIN    the olivares binary (engine and CLI); WORK an empty or new directory (a previous run's is emptied)
  STEPS  comma list of cumulative live-session targets, e.g. 5,50,128,129,200,600
"""
import argparse, datetime, hashlib, json, os, re, secrets, shutil, signal, socket, ssl, subprocess, sys, threading, time
import http.client, urllib.error, urllib.request
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

ap = argparse.ArgumentParser()
ap.add_argument('bin')
ap.add_argument('work')
ap.add_argument('steps')
ap.add_argument('--workers', type=int, default=16)
ap.add_argument('--guard-pids', type=int, default=11000, help='abort a step when the container pid count passes this')
ap.add_argument('--guard-avail-mib', type=int, default=7000, help='abort a step when MemAvailable falls below this')
ap.add_argument('--settle', type=float, default=6.0)
ap.add_argument('--max-sum-rss-mib', type=int, default=3500, help='stop short of this summed job RSS (the wrapper kills at 4096)')
ap.add_argument('--reply-timeout', type=float, default=60.0)
ap.add_argument('--turn-sample', type=int, default=40, help='sessions whose reply latency is polled')
A = ap.parse_args()

BIN = Path(A.bin)
WORK = Path(A.work)
OUT = WORK / 'evidence'
STANDIN = Path(__file__).resolve().parent / 'loadtest-sessions-standin.sh'
PIDS = Path(os.environ.get('LOAD_PIDS_FILE', str(WORK / 'procs.pids')))
MARKER = '.loadtest-sessions'
if WORK.exists() and any(WORK.iterdir()) and not (WORK / MARKER).exists():
    sys.exit(f'{WORK} is not empty and is not a previous run of this script; give an empty or new directory')
shutil.rmtree(WORK, ignore_errors=True)
for d in ('evidence', 'home', 'cli-home', 'tmp', 'data', 'folders'):
    (WORK / d).mkdir(parents=True)
(WORK / MARKER).touch()
result = {'binary': str(BIN), 'binary_sha256': hashlib.file_digest(open(BIN, 'rb'), 'sha256').hexdigest(),
          'standin_sha256': hashlib.file_digest(open(STANDIN, 'rb'), 'sha256').hexdigest(),
          'host': {}, 'steps': []}
redact = []
PAGE = os.sysconf('SC_PAGE_SIZE')
CLK = os.sysconf('SC_CLK_TCK')


def now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def save():
    text = json.dumps(result, indent=1)
    for v in redact:
        text = text.replace(v, '[REDACTED]')
    (OUT / 'result.json').write_text(text + '\n')


def free_port():
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]


def pct(xs, q):
    if not xs:
        return None
    xs = sorted(xs)
    return round(xs[min(len(xs) - 1, int(round(q * (len(xs) - 1))))], 4)


def dist(xs):
    return {'n': len(xs), 'p50': pct(xs, .5), 'p90': pct(xs, .9), 'p99': pct(xs, .99), 'max': round(max(xs), 4) if xs else None}


# ---- /proc sampling: only our own engine and its descendants, stat/statm/status, never cmdline/environ ----
def proc_table():
    t = {}
    for name in os.listdir('/proc'):
        if not name.isdigit():
            continue
        try:
            s = open(f'/proc/{name}/stat').read()
        except OSError:
            continue
        rest = s[s.rindex(')') + 2:].split()
        t[int(name)] = int(rest[1])  # ppid
    return t


def descendants(root):
    t = proc_table()
    kids = {}
    for pid, ppid in t.items():
        kids.setdefault(ppid, []).append(pid)
    out, stack = [], [root]
    while stack:
        p = stack.pop()
        for c in kids.get(p, []):
            out.append(c)
            stack.append(c)
    return out


def rss_mib(pid):
    try:
        return int(open(f'/proc/{pid}/statm').read().split()[1]) * PAGE / 1048576
    except (OSError, IndexError):
        return 0.0


def pss_mib(pid):
    try:
        for line in open(f'/proc/{pid}/smaps_rollup'):
            if line.startswith('Pss:'):
                return int(line.split()[1]) / 1024
    except OSError:
        pass
    return 0.0


def cpu_s(pid):
    try:
        rest = open(f'/proc/{pid}/stat').read()
        f = rest[rest.rindex(')') + 2:].split()
        return (int(f[11]) + int(f[12])) / CLK
    except (OSError, IndexError):
        return 0.0


def meminfo_avail_mib():
    for line in open('/proc/meminfo'):
        if line.startswith('MemAvailable:'):
            return int(line.split()[1]) / 1024


def container_pids():
    try:
        return int(open('/sys/fs/cgroup/pids.current').read())
    except OSError:  # no pids cgroup (a VM): every process the host runs
        return sum(1 for n in os.listdir('/proc') if n.isdigit())


def comm(pid):
    try:
        return open(f'/proc/{pid}/comm').read().strip()
    except OSError:
        return '?'


def sample(engine_pid):
    kids = descendants(engine_pid)
    by = {}
    for k in kids:
        c = comm(k)
        d = by.setdefault(c, {'n': 0, 'rss_mib': 0.0, 'pss_mib': 0.0})
        d['n'] += 1
        d['rss_mib'] += rss_mib(k)
        d['pss_mib'] += pss_mib(k)
    for d in by.values():
        d['rss_mib'] = round(d['rss_mib'], 1)
        d['pss_mib'] = round(d['pss_mib'], 1)
    try:
        threads = len(os.listdir(f'/proc/{engine_pid}/task'))
        fds = len(os.listdir(f'/proc/{engine_pid}/fd'))
    except OSError:
        threads = fds = -1
    return {'engine_rss_mib': round(rss_mib(engine_pid), 1), 'engine_pss_mib': round(pss_mib(engine_pid), 1),
            'engine_threads': threads, 'engine_fds': fds, 'engine_cpu_s': round(cpu_s(engine_pid), 2),
            'children_cpu_s': round(sum(cpu_s(k) for k in kids), 2), 'children_total': len(kids),
            'children_by_comm': by, 'container_pids': container_pids(), 'mem_available_mib': round(meminfo_avail_mib()),
            'load1': os.getloadavg()[0]}


# ---- engine + REST ----
http_port, grpc_port = free_port(), free_port()
BASE = f'https://127.0.0.1:{http_port}'
DATA = WORK / 'data'
ctx = None
token = tenant = ''
procs = []
engine_env = {'HOME': str(WORK / 'home'), 'PATH': os.environ['PATH'], 'LANG': 'C.UTF-8', 'TMPDIR': str(WORK / 'tmp'),
              'OLIVARES_SESSION_RUNTIME_CLAUDE_BIN': str(STANDIN)}
cli_env = {'HOME': str(WORK / 'cli-home'), 'PATH': os.environ['PATH'], 'LANG': 'C.UTF-8', 'TMPDIR': str(WORK / 'tmp'),
           'OLIVARES_CLI_CONFIG': str(WORK / 'cli.yaml')}


def spawn(args, env, log):
    p = subprocess.Popen(args, env=env, stdout=open(log, 'w'), stderr=subprocess.STDOUT, start_new_session=True)
    procs.append(p)
    PIDS.write_text(''.join(f'{q.pid} {q.pid}\n' for q in procs))
    return p


def stop_all():
    for p in reversed(procs):
        if p.poll() is None:
            try:
                os.killpg(p.pid, signal.SIGTERM)
                p.wait(60)
            except subprocess.TimeoutExpired:
                os.killpg(p.pid, signal.SIGKILL)
                p.wait()
            except ProcessLookupError:
                pass
    PIDS.unlink(missing_ok=True)


def call(method, path, body=None, auth=True, timeout=120):
    req = urllib.request.Request(BASE + path, method=method, data=None if body is None else json.dumps(body).encode())
    req.add_header('Content-Type', 'application/json')
    if auth:
        req.add_header('Authorization', 'Bearer ' + token)
        req.add_header('X-Olivares-Tenant', tenant)
    t0 = time.perf_counter()
    try:
        with urllib.request.urlopen(req, context=ctx, timeout=timeout) as r:
            raw = r.read()
            return r.status, (json.loads(raw) if raw else {}), time.perf_counter() - t0
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            body = json.loads(raw or b'{}')
        except ValueError:
            body = {'raw': raw.decode(errors='replace')[:300]}
        return e.code, body, time.perf_counter() - t0
    except (urllib.error.URLError, OSError) as e:
        return 0, {'error': str(e)[:200]}, time.perf_counter() - t0


def cli(args, stdin=None, timeout=180):
    p = subprocess.run([str(BIN), *args], env=cli_env, input=stdin, capture_output=True, text=True, timeout=timeout)
    return p


def start_session(i, account):
    folder = WORK / 'folders' / f's{i:04d}'
    folder.mkdir(exist_ok=True)
    t0 = time.perf_counter()
    st, ws, _ = call('POST', '/v1/m/sessions/workspaces', {'root_path': str(folder)})
    if st not in (200, 201):
        return {'i': i, 'ok': False, 'stage': 'workspace', 'status': st, 'body': ws, 'sec': time.perf_counter() - t0}
    st, run, _ = call('POST', '/v1/m/sessions/runs', {
        'name': f'load-{i:04d}', 'transport': 'stream-json', 'permission_mode': 'default', 'isolation': 'native',
        'workspace_ref': ws['workspace_ref'], 'provider_profile_ref': account})
    sec = time.perf_counter() - t0
    if st not in (201, 202):
        return {'i': i, 'ok': False, 'stage': 'run', 'status': st, 'body': run, 'sec': sec}
    return {'i': i, 'ok': True, 'run_ref': run.get('run_ref'), 'state': run.get('state'), 'sec': sec, 'started': time.perf_counter()}


def run_state(ref):
    st, body, _ = call('GET', f'/v1/m/sessions/runs/{ref}')
    return st, body


try:
    engine = spawn([str(BIN), 'serve', '--listen', f'127.0.0.1:{http_port}', '--grpc-listen', f'127.0.0.1:{grpc_port}',
                    '--data-dir', str(DATA)], engine_env, OUT / 'server.log')
    for _ in range(240):
        if engine.poll() is not None:
            raise RuntimeError('engine exited during start')
        if (DATA / 'tls.crt').exists():
            try:
                ctx = ssl.create_default_context(cafile=str(DATA / 'tls.crt'))
                with urllib.request.urlopen(BASE + '/readyz', context=ctx, timeout=1) as r:
                    if r.status == 200:
                        break
            except (OSError, urllib.error.URLError):
                pass
        time.sleep(0.5)
    else:
        raise RuntimeError('engine not ready')
    result['engine_exe_sha256'] = hashlib.file_digest(open(f'/proc/{engine.pid}/exe', 'rb'), 'sha256').hexdigest()
    result['engine_matches_binary'] = result['engine_exe_sha256'] == result['binary_sha256']
    setup = re.search(r'olst_[A-Za-z0-9]+', (OUT / 'server.log').read_text()).group(0)
    redact.append(setup)
    password = secrets.token_urlsafe(24)
    redact.append(password)
    st, _, _ = call('POST', '/v1/setup', {'token': setup, 'email': 'load-admin@olivares.ai', 'password': password,
                                          'organization': 'Load fixture'}, auth=False)
    assert st == 201, f'setup {st}'
    st, login, _ = call('POST', '/v1/auth/login', {'email': 'load-admin@olivares.ai', 'password': password}, auth=False)
    token = login['token']
    redact.append(token)
    st, who, _ = call('GET', '/v1/auth/whoami')
    tenant = who['grants'][0]['tenant']
    (WORK / 'token').write_text(token)
    os.chmod(WORK / 'token', 0o600)
    p = cli(['auth', 'login', '--server', BASE, '--ca-cert', str(DATA / 'tls.crt'), '--tenant', tenant,
             '--token-file', str(WORK / 'token'), '--context', 'admin'])
    (WORK / 'token').unlink()
    assert p.returncode == 0, p.stderr[-400:]
    p = cli(['provider', 'account', 'add', '--driver', 'claude', '--name', 'acct', '-o', 'json'])
    account = json.loads(p.stdout).get('account_ref', '')
    assert account, p.stdout[-400:] + p.stderr[-400:]
    p = cli(['tool', 'login', 'claude', '--account', 'acct'], stdin='stub-load\n')
    assert p.returncode == 0, p.stdout[-300:] + p.stderr[-300:]
    result['host'] = {'cpus': os.cpu_count(), 'container_pids_max': open('/sys/fs/cgroup/pids.max').read().strip()
                      if os.path.exists('/sys/fs/cgroup/pids.max') else None}
    base = sample(engine.pid)
    result['baseline'] = base
    save()

    def job_rss_mib(epid):
        return rss_mib(epid) + sum(rss_mib(k) for k in descendants(epid)) + rss_mib(os.getpid())

    hist = [(0, job_rss_mib(engine.pid))]  # (live sessions, summed job RSS MiB) at each settled point
    live = []  # run_refs of live sessions
    nxt = 1
    for target in [int(x) for x in A.steps.split(',')]:
        step = {'target': target, 'at': now()}
        want = target - len(live)
        if want <= 0:
            continue
        # The wrapper stops a job whose processes' summed RSS passes 4 GiB: project this step from the last two
        # measured points and stop short of the ceiling instead of being killed in the middle of a step.
        cur = job_rss_mib(engine.pid)
        if len(hist) >= 2 and hist[-1][0] > hist[-2][0]:
            per = (hist[-1][1] - hist[-2][1]) / (hist[-1][0] - hist[-2][0])
        elif live:
            per = (cur - hist[0][1]) / len(live)
        else:
            per = 0.0
        step['projected_sum_rss_mib'] = round(cur + per * want)
        step['marginal_rss_mib_per_session'] = round(per, 3)
        if per > 0 and cur + per * want > A.max_sum_rss_mib:
            fit = int((A.max_sum_rss_mib - cur) / per)
            step['capped_by_rss'] = {'asked': want, 'fits': max(fit, 0), 'ceiling_mib': A.max_sum_rss_mib}
            want = max(fit, 0)
            if want < 1:
                result['steps'].append(step)
                break
            target = len(live) + want
            step['target'] = target
        if container_pids() > A.guard_pids or meminfo_avail_mib() < A.guard_avail_mib:
            step['aborted'] = f'guard before step: pids={container_pids()} avail_mib={meminfo_avail_mib():.0f}'
            result['steps'].append(step)
            break
        idx = list(range(nxt, nxt + want))
        nxt += want
        cpu0 = cpu_s(engine.pid)
        t0 = time.perf_counter()
        stop_flag = threading.Event()

        def guarded(i):
            if stop_flag.is_set():
                return {'i': i, 'ok': False, 'stage': 'guard', 'status': 0, 'sec': 0}
            if container_pids() > A.guard_pids or meminfo_avail_mib() < A.guard_avail_mib:
                stop_flag.set()
                return {'i': i, 'ok': False, 'stage': 'guard', 'status': 0, 'sec': 0}
            return start_session(i, account)

        with ThreadPoolExecutor(A.workers) as ex:
            res = list(ex.map(guarded, idx))
        step['start_wall_s'] = round(time.perf_counter() - t0, 2)
        step['start_cpu_s'] = round(cpu_s(engine.pid) - cpu0, 2)
        okr = [r for r in res if r['ok']]
        bad = [r for r in res if not r['ok']]
        step['start_latency_s'] = dist([r['sec'] for r in okr])
        step['start_ok'] = len(okr)
        step['start_failed'] = len(bad)
        step['start_failures'] = [{k: r.get(k) for k in ('i', 'stage', 'status', 'body')} for r in bad[:5]]
        step['guard_tripped'] = stop_flag.is_set()
        # time until each new run reports running
        deadline = time.time() + 120
        pending = {r['run_ref']: r for r in okr}
        ready_s = []
        states = {}
        while pending and time.time() < deadline:
            def chk(ref):
                st, b = run_state(ref)
                return ref, (b.get('state') if st == 200 else f'http{st}')
            with ThreadPoolExecutor(A.workers) as ex:
                for ref, s in ex.map(chk, list(pending)):
                    states[ref] = s
                    if s in ('running', 'idle'):
                        ready_s.append(time.perf_counter() - pending.pop(ref)['started'])
                    elif s in ('failed', 'stopped', 'completed'):
                        pending.pop(ref)
            if pending:
                time.sleep(0.5)
        step['time_to_running_s'] = dist(ready_s)
        step['state_counts'] = {}
        for s in states.values():
            step['state_counts'][s] = step['state_counts'].get(s, 0) + 1
        live += [r['run_ref'] for r in okr if states.get(r['run_ref']) in ('running', 'idle')]
        step['live_after'] = len(live)
        time.sleep(A.settle)
        step['sample_idle'] = sample(engine.pid)
        step['job_sum_rss_mib'] = round(job_rss_mib(engine.pid), 1)
        hist.append((len(live), step['job_sum_rss_mib']))
        # API answers with N live sessions
        lat = []
        for _ in range(30):
            st, b, sec = call('GET', '/v1/m/sessions/runs?limit=100')
            lat.append(sec if st == 200 else 99.0)
        step['list_runs_limit100_s'] = dist(lat)
        lat = []
        for ref in live[-30:]:
            st, b, sec = call('GET', f'/v1/m/sessions/runs/{ref}')
            lat.append(sec if st == 200 else 99.0)
        step['get_run_s'] = dist(lat)
        lat = []
        for _ in range(30):
            _, _, sec = call('GET', '/readyz', auth=False)
            lat.append(sec)
        step['readyz_s'] = dist(lat)
        # a turn to every live session at once; the reply is timed from the output stream of a sample
        def attach_wait(ref, needle, box, ready):
            conn = http.client.HTTPSConnection('127.0.0.1', http_port, context=ctx, timeout=A.reply_timeout)
            try:
                conn.request('GET', f'/v1/m/sessions/runs/{ref}/attach', headers={
                    'Authorization': 'Bearer ' + token, 'X-Olivares-Tenant': tenant, 'Accept': 'text/event-stream'})
                resp = conn.getresponse()
                ready.set()
                seen = box.setdefault('seen', [])
                while True:
                    line = resp.readline()
                    if not line:
                        break
                    if len(seen) < 6:
                        seen.append(line[:260].decode(errors='replace'))
                    if needle in line:
                        box['recv'] = time.perf_counter()
                        break
            except Exception as e:  # reported below as a missing reply with its reason
                box['err'] = str(e)[:120]
                ready.set()
            finally:
                conn.close()

        sample_refs = live[-A.turn_sample:]
        watchers = {}
        for ref in sample_refs:
            box, ready = {}, threading.Event()
            needle = f'ping-{target}-{ref[-8:]}'.encode()
            th = threading.Thread(target=attach_wait, args=(ref, needle, box, ready), daemon=True)
            th.start()
            watchers[ref] = (th, box, ready)
        for th, box, ready in watchers.values():
            ready.wait(30)

        sent = {}

        def turn(ref):
            line = json.dumps({'type': 'user', 'message': {'role': 'user', 'content': f'ping-{target}-{ref[-8:]}'}}, separators=(',', ':'))
            t0 = time.perf_counter()
            st, b, sec = call('POST', f'/v1/m/sessions/runs/{ref}/input', {'line': line})
            sent[ref] = t0
            return ref, st, sec
        cpu1 = cpu_s(engine.pid)
        t1 = time.perf_counter()
        with ThreadPoolExecutor(A.workers) as ex:
            turns = list(ex.map(turn, live))
        step['turn_wave_wall_s'] = round(time.perf_counter() - t1, 2)
        step['turn_wave_cpu_s'] = round(cpu_s(engine.pid) - cpu1, 2)
        step['turn_post_s'] = dist([t[2] for t in turns if t[1] == 202])
        step['turn_post_failed'] = sum(1 for t in turns if t[1] != 202)
        reply_s, errs = [], []
        for ref, (th, box, ready) in watchers.items():
            th.join(A.reply_timeout + 5)
            if 'recv' in box:
                reply_s.append(box['recv'] - sent[ref])
            else:
                errs.append(box.get('err', f'no frame within {A.reply_timeout:.0f} s'))
                step.setdefault('reply_probe', box.get('seen'))
        step['reply_visible_s'] = dist(reply_s)
        step['reply_missing'] = len(errs)
        step['reply_missing_reasons'] = sorted(set(errs))[:3]
        step['sample_after_turns'] = sample(engine.pid)
        result['steps'].append(step)
        save()
        print(json.dumps({k: step.get(k) for k in ('target', 'live_after', 'start_failed', 'time_to_running_s')}), flush=True)
        if stop_flag.is_set() or step['start_failed']:
            break

    # stop wave and leftover check
    t0 = time.perf_counter()

    def stop(ref):
        st, _, sec = call('POST', f'/v1/m/sessions/runs/{ref}/stop')
        return st, sec
    with ThreadPoolExecutor(A.workers) as ex:
        stops = list(ex.map(stop, live))
    result['stop_wave'] = {'wall_s': round(time.perf_counter() - t0, 2), 'n': len(live), 'latency_s': dist([s[1] for s in stops]),
                           'failed': sum(1 for s in stops if s[0] not in (200, 202, 204))}
    time.sleep(A.settle)
    result['after_stop'] = sample(engine.pid)
    save()
finally:
    stop_all()
    result['finished'] = now()
    save()
print('done', json.dumps({'steps': [s.get('target') for s in result['steps']]}))
