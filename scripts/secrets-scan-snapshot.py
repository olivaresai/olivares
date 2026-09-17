#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Capture ONCE the input of the all-ref secrets sweep, so the guard, the scan and the
attribution read the same subject (SG3 F1: a ref landing between guard and scan was scanned
under an exemption nobody proved).

usage: secrets-scan-snapshot.py <empty owned dir>     (cwd = the checkout being judged)

Builds, inside <dir>, a Git metadata snapshot that shares the live objects READ-ONLY:
  objects/info/alternates  -> the live object directory (nothing is copied)
  packed-refs              -> every ref `git for-each-ref` shows, same names (labels kept)
  HEAD                     -> the checkout's HEAD, detached at the captured object
  tips                     -> HEADs of every worktree, which live `--all` adds and refs do not
  config                   -> the EFFECTIVE config (all scopes, last wins), minus repository
                              structure and credential-bearing keys; attributes files copied
  info/attributes          -> root .gitattributes of the checkout, then the live info/attributes
  gitleaks.toml            -> the exact config bytes the guard and the scanner will read
Children must run with GIT_DIR=<dir> GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null, so a
later change to the live refs, config or attributes cannot reach them.

Prints key=value lines and exits 0, or exits non-zero with the reason on stderr: an input this
construction cannot freeze (nested .gitattributes, [extend] path/url, shallow grafts) is a
refusal, never a partial snapshot.
"""
import hashlib, os, re, subprocess, sys

snap = os.path.abspath(sys.argv[1])
SKIP = re.compile(r'^(core\.(bare|worktree|repositoryformatversion|attributesfile)|extensions\..*|include\..*|includeif\..*'
                  r'|http\..*|credential\..*|url\..*|remote\..*|branch\..*)$')


def fail(msg):
    sys.stderr.write('secrets-scan-snapshot: %s\n' % msg)
    sys.exit(3)


def git(*args):
    p = subprocess.run(['git', *args], capture_output=True)
    if p.returncode != 0:
        fail('git %s failed' % args[0])
    return p.stdout


common = git('rev-parse', '--path-format=absolute', '--git-common-dir').strip().decode()
objdir = git('rev-parse', '--path-format=absolute', '--git-path', 'objects').strip().decode()
fmt = git('rev-parse', '--show-object-format').strip().decode()
if os.path.exists(os.path.join(common, 'shallow')) or os.path.exists(os.path.join(common, 'info', 'grafts')):
    fail('shallow or grafted history cannot be frozen by this construction')
tracked_attrs = [p for p in git('ls-files', '-z', '--', ':(glob)**/.gitattributes').split(b'\0') if p]
if any(p != b'.gitattributes' for p in tracked_attrs):
    fail('nested .gitattributes cannot be frozen by this construction')
try:
    import tomllib
    cfg_bytes = open('.gitleaks.toml', 'rb').read()
    cfg = tomllib.loads(cfg_bytes.decode('utf-8'))
except Exception:
    fail('.gitleaks.toml cannot be read or parsed here')
ext = cfg.get('extend') if isinstance(cfg.get('extend'), dict) else {}
if ext.get('path') or ext.get('url'):
    fail('[extend] path/url pulls in a config file this snapshot does not freeze')

refs = []
# show-ref, not for-each-ref: the same refs under refs/, and the attribution keeps its single census
shown = subprocess.run(['git', 'show-ref'], capture_output=True)
if shown.returncode not in (0, 1) or (shown.returncode == 1 and shown.stdout):
    fail('git show-ref failed')
for line in shown.stdout.splitlines():
    oid, _, name = line.partition(b' ')
    if not oid or not name:
        fail('unreadable for-each-ref line')
    refs.append((name, oid))
refs.sort()
head = subprocess.run(['git', 'rev-parse', '--verify', '--quiet', 'HEAD'], capture_output=True).stdout.strip()
tips = set()
for rec in git('worktree', 'list', '--porcelain', '-z').split(b'\0\0'):
    for tok in rec.split(b'\0'):
        if tok.startswith(b'HEAD ') and re.fullmatch(rb'[0-9a-f]{40,64}', tok[5:]) and set(tok[5:]) != {ord('0')}:
            tips.add(tok[5:])
n_heads = len(tips)
# Per-worktree refs (refs/bisect, refs/worktree, refs/rewritten). This worktree's are already in
# `show-ref` above, by name; measured on git 2.39.5 they are the only ones live `--all` reads. Every
# OTHER worktree's are added as unlabelled tips anyway, enumerated by git per worktree gitdir, so the
# subject is never smaller than any git's `--all`. Unreadable per-worktree refs are a refusal.
PER_WT = ('bisect', 'worktree', 'rewritten')
if os.path.isdir(os.path.join(common, 'reftable')):
    fail('reftable ref storage: per-worktree refs are not enumerable by this construction')
current_gitdir = os.path.realpath(git('rev-parse', '--path-format=absolute', '--absolute-git-dir').strip().decode())


def refuse_unreadable(err):
    fail('cannot read per-worktree refs under %s' % err.filename)


def entries_of(path):
    try:
        return os.listdir(path)
    except FileNotFoundError:
        return []
    except OSError as err:
        refuse_unreadable(err)


other_wt_refs = 0
for gd in [common] + [os.path.join(common, 'worktrees', n) for n in sorted(entries_of(os.path.join(common, 'worktrees')))]:
    if os.path.realpath(gd) == current_gitdir:
        continue
    present = [s for s in PER_WT if s in entries_of(os.path.join(gd, 'refs'))]
    if not present:
        continue
    for sub in present:
        for _ in os.walk(os.path.join(gd, 'refs', sub), onerror=refuse_unreadable):
            pass
    for oid in git('--git-dir=' + gd, 'for-each-ref', '--format=%(objectname)', 'refs/bisect', 'refs/worktree', 'refs/rewritten').split():
        other_wt_refs += 1
        tips.add(oid)
tips = sorted(tips)

entries = []
for rec in git('config', '--list', '-z').split(b'\0'):
    if not rec:
        continue
    key, nl, value = rec.partition(b'\n')
    entries.append((key.decode('utf-8', 'surrogateescape'), value.decode('utf-8', 'surrogateescape') if nl else 'true'))
attrs_global = None
for key, value in entries:
    if key == 'core.attributesfile':
        attrs_global = os.path.expanduser(value)
if attrs_global is None:
    xdg = os.environ.get('XDG_CONFIG_HOME') or os.path.join(os.path.expanduser('~'), '.config')
    attrs_global = os.path.join(xdg, 'git', 'attributes')


def q(v):
    return '"' + v.replace('\\', '\\\\').replace('"', '\\"').replace('\n', '\\n').replace('\t', '\\t') + '"'


try:
    for d in ('objects/info', 'refs', 'info'):
        os.makedirs(os.path.join(snap, d), exist_ok=True)
    with open(os.path.join(snap, 'objects', 'info', 'alternates'), 'w') as fh:
        fh.write(objdir + '\n')
        for extra in filter(None, os.environ.get('GIT_ALTERNATE_OBJECT_DIRECTORIES', '').split(os.pathsep)):
            fh.write(os.path.abspath(extra) + '\n')
    with open(os.path.join(snap, 'packed-refs'), 'wb') as fh:
        fh.write(b'# pack-refs with: sorted \n')
        for name, oid in refs:
            fh.write(oid + b' ' + name + b'\n')
    with open(os.path.join(snap, 'HEAD'), 'wb') as fh:
        fh.write(head + b'\n' if head else b'ref: refs/heads/check-secrets-unborn-head\n')
    with open(os.path.join(snap, 'tips'), 'wb') as fh:
        fh.write(b''.join(t + b'\n' for t in tips))
    gattr = os.path.join(snap, 'attributes.global')
    with open(gattr, 'wb') as fh:
        if os.path.isfile(attrs_global):
            fh.write(open(attrs_global, 'rb').read())
    with open(os.path.join(snap, 'info', 'attributes'), 'wb') as fh:
        if os.path.isfile('.gitattributes'):
            fh.write(open('.gitattributes', 'rb').read() + b'\n')
        live_info = os.path.join(common, 'info', 'attributes')
        if os.path.isfile(live_info):
            fh.write(open(live_info, 'rb').read())
    with open(os.path.join(snap, 'config'), 'w', encoding='utf-8', errors='surrogateescape') as fh:
        fh.write('[core]\n\trepositoryformatversion = %d\n\tbare = true\n\tattributesfile = %s\n' % (1 if fmt != 'sha1' else 0, q(gattr)))
        if fmt != 'sha1':
            fh.write('[extensions]\n\tobjectformat = %s\n' % fmt)
        for key, value in entries:
            if SKIP.match(key):
                continue
            section, _, rest = key.partition('.')
            sub, dot, name = rest.rpartition('.')
            head_line = '[%s %s]' % (section, q(sub)) if dot else '[%s]' % section
            fh.write('%s\n\t%s = %s\n' % (head_line, name if dot else rest, q(value)))
    with open(os.path.join(snap, 'gitleaks.toml'), 'wb') as fh:
        fh.write(cfg_bytes)
except OSError as e:
    fail('cannot write the snapshot (%s)' % e.strerror)

env = dict(os.environ, GIT_DIR=snap, GIT_CONFIG_NOSYSTEM='1', GIT_CONFIG_GLOBAL=os.devnull)
env.pop('GIT_CONFIG_PARAMETERS', None); env.pop('GIT_CONFIG_COUNT', None); env.pop('GIT_WORK_TREE', None)
probe = subprocess.run(['git', 'rev-parse', '--git-dir'], capture_output=True, env=env)
if probe.returncode != 0 or (head and subprocess.run(['git', 'cat-file', '-e', head.decode() + '^{commit}'], env=env).returncode != 0):
    fail('the snapshot cannot be read back')
renames = subprocess.run(['git', 'config', '--get', 'diff.renames'], capture_output=True, env=env)
digest = hashlib.sha256(open(os.path.join(snap, 'packed-refs'), 'rb').read() + b'HEAD ' + head + b'\n' + b''.join(tips)).hexdigest()
print('head=%s' % (head.decode() or 'unborn'))
print('refs=%d' % len(refs))
print('worktree_heads=%d' % n_heads)
print('other_worktree_ref_tips=%d' % other_wt_refs)
print('refs_sha256=%s' % digest)
print('config_sha256=%s' % hashlib.sha256(cfg_bytes).hexdigest())
print('diff_renames=%s' % (renames.stdout.strip().decode() if renames.returncode == 0 else 'unset'))
