# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Compare migration content hashes against Git, never a rewritable checksum list."""
import hashlib
import io
import json
import os
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]

# A migration no database ever applied may leave the tree once, by exact path and baseline content.
# commercial/license-worker/migrations/0063_instance_lease.sql was this repository's draft of the
# Business instance lease. The production and sandbox D1 ledgers (d1_migrations, read 2026-10-12)
# never applied it; production applied its own 0063 and its instance leases are 0074.
NEVER_APPLIED = {
    'commercial/license-worker/migrations/0063_instance_lease.sql':
        '1d654343b1f36fff13791daa7ade458f927e119dd50cfbf61fff7a843ae02522',
}

# Bindings whose digest moved while no applied migration is read differently, accepted only as exact reviewed
# pairs (baseline digest, new digest). The first pair is origin/main 7a241bb74; the second is the public 0.4 export.
# the regression moved the temp_store pragma constant and the _pragma DSN helper out of store.go into
# core/dr/opgate/sqlitepragmas.go so the DR snapshot's source open can share them; the store's three opens build
# byte-identical DSNs through the opgate names. The 0.4 pair also covers the guard metadata cache, the later
# error-wrapper and current-registry refactors, and afb4bec77's variadic SetOrgSettings concurrency check, none of
# which changes migration inputs or applied content. The third and fourth pairs (origin/main f4e128e6b, public 0.4) are
# the regression: bootGuardCensus, which openPrepared calls before the migration plan, also selects the census without the
# AI terms pair for a directory an earlier Community release wrote; no migration statement, plan or callback changes.
# The final pairs bind the boot census to the reviewed AI terms profile transition.
REVIEWED_BINDINGS = {
    'core/binding/core/internal/store/sqlstore/store.go/openPrepared/buildCoreMigrationPlan/1': (
        ('07c7ca1548c23d82288a3184efe7e02a1b142a316f77fcc072ffb6ce907b19b4',
         'c0061ac4e3965ca0a230023cfeb956c5a843787c00eb461c80a120db234887cb'),
        ('f348a0c80cd65335f5f8c422a46b7ef1a615dc38ea8500aed9e646382de6d14c',
         'aff9a3a853e4cb6618dbd9c0174755fc26e9f3f8ca6a5066955020668e432eeb'),
        ('b4c6e18260e23fad75f51436ee9a37627ade1bbf6b75492d3c1e050397edcafb',
         'b1e6c17e8304ea90fce7cf8d4d4b45e70171e6ee674a1d1bc2c22c8a5fdb68a3'),
        ('f348a0c80cd65335f5f8c422a46b7ef1a615dc38ea8500aed9e646382de6d14c',
         'b910f49145895a04b9378c6c2c3c8810e9b75070c90283c3b3d4ec4d04b54ee4'),
        ('b1e6c17e8304ea90fce7cf8d4d4b45e70171e6ee674a1d1bc2c22c8a5fdb68a3',
         '33fd1813557f335c77148596632848f266fd8c4e670ddcc38c0ca9c98aa507d3'),
        ('f348a0c80cd65335f5f8c422a46b7ef1a615dc38ea8500aed9e646382de6d14c',
         '73036cf26ed0371af0696f2106d3fdd0b4c6af30748a5b22411e220f1a71cff2'),
    ),
    'core/binding/core/internal/store/sqlstore/store.go/openPrepared/buildCoreMigrations/1': (
        ('d6916f0bf171360672387c95e4b5a48291871c238aafa8f5037ea67b6b67df34',
         'a998fa92501003913f626839fece82b898ff35082f3a5a9c206a8a93029cfbf5'),
        ('f7264bf98632902afa007091ba78aa1dbca1aa57edea3964a5fec73aa882273f',
         '440cec34f56451a5198a639f7b224d0b4784972cc5b8cc306f17f4bc2557954e'),
        ('51c1d2bf82c0887de4c70bc46df1790e7fa3b198ceaa5f93684e21de8ba1b265',
         '16bcc081903a11b0fdfccbd689f7b13588d7ba327a3b64f9dec6e3f3f3a849cb'),
        ('f7264bf98632902afa007091ba78aa1dbca1aa57edea3964a5fec73aa882273f',
         '38223605d59ab85f7dfd5c0963a3550e82af0fd4fcfda3fb5eb47d8d70580cf8'),
        ('16bcc081903a11b0fdfccbd689f7b13588d7ba327a3b64f9dec6e3f3f3a849cb',
         'd2ba848889a9602d7375334cc201b925da0fd2e12ee33a26513422fa0f151841'),
        ('f7264bf98632902afa007091ba78aa1dbca1aa57edea3964a5fec73aa882273f',
         '75bb181a5a5e7ac35712d7e36b2e161e7a487595b2a4a2ae408ce707a8fb0369'),
    ),
}

# Each migration key can carry multiple exact reviewed pairs, as bindings already do.
# The additional pairs bind the profile transition from pre-AI-terms Community through
# current Community to Business. Constructor-reference hashing reaches the shared graph
# from buildCoreMigrations, even for versions whose SQL statements are unchanged.
# Historical pairs remain valid; unknown digests and SQL changes still refuse.
# guardMetadataSpecs gained a clone-on-read cache for the compiled format. Its fresh derivation and cached result are
# asserted equal by TestGuardMetadataSpecsAreDerivedOnce, and the migration/variant suites prove the same reads. afb4bec77
# adds a variadic expectedVersion to SetOrgSettings, a concurrency check that is not a migration input. These exact pairs
# admit only those behavior-preserving source changes, not changed migrations.
REVIEWED_MIGRATIONS = {
    'core/1': (
        ('bc9a5e27315cc700fcd890c0dfefec5f96c8b144b9e4c91efd4443683573aa53',
         'aa887d4d437bdd5f7aec8d26e4e94d4d36d602f2d39d6b063b6455eb7a7c7bf6'),
        ('004859b10f95bb7163c0337c8380d96cf32e284b07b697be253b1e8288fbf39a',
         'a534d53f4a7589e30045bee526ea60328ae6898252768ff6a103a2ac37bfb1d8'),
    ),
    'core/2': (
        ('71942827679f3eeae3b7589cc8aed81c33d62aa2a99ebc6b5e4dac8e7066e544',
         'c6897a066298ef9c926ac119157bbacd343e3682d49f60bd321720981d594a5f'),
        ('c380be05ae6274be555a7c97ca8a02c7b327de72c73873abd4a24f43cc73c760',
         '1761090a3a19c646c435723d2d4bc224d89db359985c409eae2db64a758d20ce'),
    ),
    'core/3': (
        ('5ee1843b11004b240e1c74d9c248949b78f67104bbec8c3a37f2d26723be2918',
         'b5d1f30999400ec981ac505a97587ad264e7f92701260ed116c424357c7bf2c3'),
        ('c7edffc8f3727bed340506dd319b8560db97997088035b18d60f02dcd6dacafd',
         'dbfc54a576c3edb603a0d2622f6a0b5d3e16f252837a4bf66b3cbe985f3f4968'),
    ),
    'core/4': (
        ('66b7684292ef81d0b934faa0a896aa0137057fac2ad7c8a245f3728c9b391161',
         '430b3c14716b349cee5ada8534cd46e0a385ede02c1b10e09a8278585fca1157'),
        ('b14e95087f7a2cfafed705296871e7a873da4259ea1e47c5fb474d9482f634e9',
         'a1e461751f5527951a98e07c5fea9452b6b3e89a7c343f179baaf159a1279ed6'),
    ),
    'core/5': (
        ('b58ee7949cb3d737379716fe9259ecae0ab76bc94aa2232104256eac95ec9ccf',
         '3f71139ead1b476197539674dbf4dd0224a8096b89706d4ee8d5c61c0cf8f1de'),
        ('e3cda396761823d6a71a78fd5d2168c7279ee44d67de6622af0483f12d5b20b2',
         'd3b1768214829fd766fc04ed5ac6bd8e160e826cd23dfde415be7f017ccdd30e'),
    ),
    'core/6': (
        ('d4b8fd1ffb453430088cfc14a51cfc2392d2d37e767152aaf46a8fc75ebce931',
         'cc82a1e36d6e4f1c73710096e03bf508b615a6b89a6c23b9c0c6b8f91b1ae176'),
        ('4d904b8f6c265665f75cb32b6df2b3e792e75b022a41e7e3a254052066454e90',
         'dc94c32fa7e69279c6fb5b826f2ca4da429c8d4c27ad4feb892317d1f20f2f1b'),
    ),
    'core/7': (
        ('231d8a581e6510f8f26ac8949b7707eb4c3ad82d35ed4603becabfa42eaa0352',
         '3766e9f24c314efa43618ce262fbca92235ef219fb0d829110b56e60b5c1cd02'),
        ('fb5e3f97ed9967d2fffb42309ac3df52a4d2c8adede96273f7b45bcd20a870af',
         '59f2f65871beabdf26390c44ccfd0b01a30a8b83f0697b9834939d9768419a5e'),
    ),
    'core/9': (
        ('e33d675a7e57c70c19931154b07154a31b820867063b50c1bdd9bf0bfd24c1a3',
         'd070a769e56f6680da5150f034c32ba0a2c96743865c4ccab9620e3efa219a8c'),
        ('cd4d9b9b0aa103d42bb25728b98b34278f5f2c516896574dd99eaad31e7f3ca8',
         'eb3d7cc2cde16db814741ef5cf02ab5c9c306e17d5321a82bd1b01b47e290392'),
    ),
    'core/10': (
        ('b6aa309a1d4860a777f140de8ae141849b8d8318dff63b0abda93ddda4a65cc5',
         'ebc12bdb353abed7287918c7cf27f222fc40a1a50ed7cd3e9c80ab85db40f89f'),
        ('ef4ec4f94614f36dac476a60c2d646d7cfb2efabe63a8a58a2dfa6bea002532e',
         'bfa7cf03272408daad35d40b9d7633be32903efc23d8c2a7d8b995964a2b16de'),
    ),
    'core/11': (
        ('0c0722de2b28e2122d3045635cd9a5749689f3a8047b3d1122aa65f3891cd313',
         '70665ff96e263d6dc617a8e227a555916c57d37d71b3794abf0ff170c17f6708'),
        ('1b445125df8cd2663795366f2728ee11c47c1d9ab3c97a0dc8a71c9d8e63d51f',
         'cbf65f126b52dc9a88df35a7a9d8edd83bbdbb41f4c1815394661b19d71d92d1'),
    ),
    'core/13': (
        ('2296a1cb514e63bd93bab226d039650dd86a06970cf055508af5d478a2ce86fe',
         '34b51848371a480500533d55dfea2821aff295870567e29c31e4fef783b115f4'),
        ('9c36d189b0acf09c54ffe505fee8e70f123bae75c8ce6367c87d0e77fc3ea2e3',
         '34e74b0ba11d8889c8fb9303bad88ae1571b494541cac1b1445fc3cb0d5c5e86'),
    ),
    'core/19': (
        ('47c40a76e7f35499d0f910e8106790679fc65b3ce423cbfdc4657840caffcd1b',
         'c8ab02f836c382c96f232f9ec5a7c89969dac4c30dcf7f862eb11bbb3d517a7f'),
        ('ba73a0eafda6b2bb62819f40f7a2180308b30f4f1987b7b278b380bc0503e2e6',
         '3c5d6c28c60250550ffd4f96cccb1b0bec086b20f143e89ea17f071422914132'),
    ),
    'core/26': (
        ('04f4cfd58bc6c2931037007e2a5e8e5b9b86f52290502eea40fd9ba08f74f565',
         'a8f1bba1492a077c7ce6c584db92d6871b7e7c992506317a1a2635bc160a65e8'),
        ('ab9748bdf7b9c1475a67a4b8c269f2f57cd48f01076b45860972191c3bbadea7',
         '6a3767cb73b4e9819c4c6b2f4de65f04ad75753800e1ddff23d4cc5ca7a29b62'),
    ),
    'runner/core/internal/store/sqlstore/versioned_schema.go/reconcileVersionedDirectoryGuards/"schema_migrations_directory_guards"/1': (
        ('e4294291500fcb91228b4a1fc559a9e65806a349bca6d78d2d6b6ace93e3c094',
         'f07781dda48eff96e011bac3d40a7cc870b696db5ebb3a80cc32c6e845b998be'),
        ('ef2ca3e86f3743160f3dbe404644f72c24554c3abcca459b49a3189fc2dfd589',
         '02b3220a9809d91cfd847a5ff67c2ac86d445c24646a4555104e0f23d1029595'),
    ),
}

# Base 2e6e9616: exact transitions for reverting unpublished main state to the published 0.4 schema and for the
# reviewed behavior-preserving cache, DSN helper, error-wrapper, current-registry, and variadic SetOrgSettings changes.
# No migration statement or plan input changes; every other base-to-head difference still fails.
REVIEWED_BASE = {
    'core/1': ('638632b399f14366f915f61df61e0fca9198c1c9c5b5eb5b7927bd4f32b0ab5d',
               '004859b10f95bb7163c0337c8380d96cf32e284b07b697be253b1e8288fbf39a'),
    'core/2': ('ecb624e4fe07583dc3fe8816cdcfc79cb2cb856fd3e3f0b6fcaca88a29f7b5e8',
               'c380be05ae6274be555a7c97ca8a02c7b327de72c73873abd4a24f43cc73c760'),
    'core/3': ('762abf04e912be5cd241f83846621456ded9c53822d4ece95a165735ded13469',
               'c7edffc8f3727bed340506dd319b8560db97997088035b18d60f02dcd6dacafd'),
    'core/4': ('77dd7429be48308883181d6bdb59ba63d1b07e273910efc8613290f817b8dfad',
               'b14e95087f7a2cfafed705296871e7a873da4259ea1e47c5fb474d9482f634e9'),
    'core/5': ('927614355f7350fffea476f75b30da8bd8ee6cd924cc89751a1235391017042a',
               'e3cda396761823d6a71a78fd5d2168c7279ee44d67de6622af0483f12d5b20b2'),
    'core/6': ('64e1b58b9ec2c9a844a8d887fe4c680039652f2b95d7c597cd55e5d1cc458fb7',
               '4d904b8f6c265665f75cb32b6df2b3e792e75b022a41e7e3a254052066454e90'),
    'core/7': ('403c797cf92e5727a0e854f23c826d0e0c812b24f2c4d164dc3c1a13ced4e296',
               'fb5e3f97ed9967d2fffb42309ac3df52a4d2c8adede96273f7b45bcd20a870af'),
    'core/9': ('cf1c7c08d1e3fa8e2fe31be07dcc6b856aa8aacbc41bd6fae57ef4a568f0cdda',
               'cd4d9b9b0aa103d42bb25728b98b34278f5f2c516896574dd99eaad31e7f3ca8'),
    'core/10': ('e46d7e756aedcaf508831261e1e4474cf12e12c76c8cec345848651091d667a8',
                'ef4ec4f94614f36dac476a60c2d646d7cfb2efabe63a8a58a2dfa6bea002532e'),
    'core/21': ('544105e68788fe062ed3a5f24704786dd821e0175d9edfd32a06b22daf930b62',
                '8833a3c0600474d931512a0a384f0adacfb2bb00112a9318b0cda7f19e076243'),
    'core/28': ('6bea9c39157c661ad4ecd6a840ecf549aae24390aacd08d9a521a65818bebb4d',
                '06f4880a61dfd1abbfc4275e68c935b2a3c1dbd3d38ac1cb0cb6ba96a3108100'),
    'core/binding/core/internal/store/sqlstore/store.go/openPrepared/buildCoreMigrationPlan/1': (
        'c7df024c57e30cf6f98efab4fc6cad3bc8ff512522534a474a3637e967ec538e',
        'b4c6e18260e23fad75f51436ee9a37627ade1bbf6b75492d3c1e050397edcafb'),
    'core/descriptor-registration/24': ('d7b1988999d0b07713bcf6305ef790faca9db523d8b5627ddb159a1a9942dafc',
                                        '0659f4c3a7ef6ce92ac7cacc3c1be4554317bcd561bddf17b877cd0f57c40535'),
    'core/descriptor/authSessionDescriptor': ('dfaa6d28e78cac5c0a82b72f2d0276392bef665ef66287bf2ce08debc7be61c5',
                                              '9a9c6647856ca2b7f19020eb21f3534dd25a2d0fa809027e561b9e8e4f5fb162'),
    'core/plan/buildCoreMigrations': ('73a844e03db76f709f0811920529d7db808ce475155b0d7f0a390b4abcd35c00',
                                      '857a6d70122953124624eb9cb9dd3d76506d6210e7cf15b5407333f6ff84f575'),
}


def git(*args):
    return subprocess.check_output(['git', *args], cwd=ROOT, stderr=subprocess.PIPE)


def relevant(name):
    parts = Path(name).parts
    return ('testdata' not in parts and 'vendor' not in parts and
            ((name.endswith('.sql') and 'migrations' in parts) or
             (name.endswith('.go') and not name.endswith('_test.go'))))


def sql_hashes(files):
    hashes = {}
    versions = {}
    for name, body in files.items():
        if not name.endswith('.sql'):
            continue
        path = Path(name)
        match = re.match(r'(\d+)_', path.name)
        if not match:
            raise ValueError('migration has no version: ' + name)
        key = (str(path.parent), int(match[1]), 'down' if name.endswith('.down.sql') else 'up')
        if key in versions:
            raise ValueError(f'duplicate migration version: {versions[key]} and {name}')
        versions[key] = name
        hashes[name] = hashlib.sha256(body).hexdigest()
    return hashes


def baseline_files(sha):
    # Read Git blobs directly: git archive honors export-ignore/export-subst,
    # which must never remove or rewrite the comparison's source of truth.
    entries = {}
    for entry in git('ls-tree', '-rz', '--full-tree', sha).split(b'\0'):
        if not entry:
            continue
        header, name = entry.split(b'\t', 1)
        name = name.decode()
        if not relevant(name):
            continue
        mode, kind, oid = header.split()
        if kind != b'blob' or mode == b'120000':
            raise ValueError('baseline migration source is not a regular file: ' + name)
        entries[name] = oid
    ids = sorted(set(entries.values()))
    result = subprocess.run(['git', 'cat-file', '--batch'], cwd=ROOT,
                            input=b'\n'.join(ids) + b'\n', capture_output=True, check=True)
    stream = io.BytesIO(result.stdout)
    blobs = {}
    for oid in ids:
        got, kind, size = stream.readline().split()
        if got != oid or kind != b'blob':
            raise ValueError('could not read baseline blob')
        blobs[oid] = stream.read(int(size))
        if len(blobs[oid]) != int(size) or stream.read(1) != b'\n':
            raise ValueError('truncated baseline blob')
    return {name: blobs[oid] for name, oid in entries.items()}


def main():
    baseline = os.environ.get('OLIVARES_MIGRATION_BASE')
    if not baseline:
        # Local branches compare with their mainline ancestor. Minimal fixture
        # repositories have no remote and compare working files with HEAD.
        refs = git('for-each-ref', '--format=%(refname)', 'refs/remotes/origin/main')
        baseline = 'HEAD'
        if refs.strip():
            # merge-base exits 1, printing nothing, when the histories share no commit: a tree with
            # its own history (the merge gate's Community export) whose origin/main is the published
            # tag. That ref is then the baseline itself. A shallow clone also finds no merge base
            # for a different reason, so it refuses instead.
            found = subprocess.run(['git', 'merge-base', 'HEAD', 'origin/main'], cwd=ROOT,
                                   capture_output=True, text=True)
            if found.returncode not in (0, 1):
                raise ValueError(found.stderr.strip() or 'git merge-base failed')
            baseline = found.stdout.strip()
            if not baseline:
                if git('rev-parse', '--is-shallow-repository').strip() == b'true':
                    raise ValueError('shallow clone: no merge base with origin/main; fetch the full history')
                baseline = 'origin/main'
            elif baseline == git('rev-parse', 'HEAD').decode().strip():
                baseline = 'HEAD^'
    sha = git('rev-parse', '--verify', baseline + '^{commit}').decode().strip()
    # A release that starts a fresh-install line (0.1: no upgrade from 26.10.x) declares the epoch in
    # core/migrate/EPOCH.txt. A baseline without that file predates it, so history restarts once; every
    # baseline that carries the file is compared in full again.
    epoch = ROOT / 'core/migrate/EPOCH.txt'
    if epoch.is_file() and subprocess.run(['git', 'cat-file', '-e', f'{sha}:core/migrate/EPOCH.txt'],
                                          cwd=ROOT, capture_output=True).returncode != 0:
        print(f'✓ migration epoch {epoch.read_text().strip()}: baseline {sha[:12]} predates it; history restarts here')
        return 0
    before = baseline_files(sha)
    paths = git('ls-files', '-z', '--cached', '--others', '--exclude-standard').decode().split('\0')
    after = {}
    for name in sorted(set(paths)):
        if name and relevant(name):
            target = ROOT / name
            if not target.exists():
                continue  # A tracked deletion must be compared against the baseline.
            if target.is_symlink():
                raise ValueError('migration source is a symlink: ' + name)
            after[name] = target.read_bytes()
    old, new = sql_hashes(before), sql_hashes(after)
    sources = [{name: body.decode() for name, body in files.items() if name.endswith('.go')}
               for files in (before, after)]
    result = subprocess.run(['go', 'run', str(ROOT / 'scripts/migrationhash/main.go')],
                            input=json.dumps(sources), text=True, capture_output=True, check=True,
                            env={**os.environ, 'GOWORK': 'off', 'GO111MODULE': 'off', 'GOTOOLCHAIN': 'local'})
    old_go, new_go = json.loads(result.stdout)
    old.update(old_go)
    new.update(new_go)
    if not old:
        raise ValueError('baseline contains no migrations')
    if any(not key.startswith('core/binding/') or key.endswith('.sql') for key in REVIEWED_BINDINGS):
        raise ValueError('REVIEWED_BINDINGS may hold binding keys only')
    if any(re.fullmatch(r'(?:core/\d+|runner/.+)', key) is None for key in REVIEWED_MIGRATIONS):
        raise ValueError('REVIEWED_MIGRATIONS may hold numeric core or runner migration keys only')
    base_key = re.compile(r'core/(?:binding/.+|\d+|descriptor-registration/\d+|descriptor/[^/]+|plan/[^/]+)$')
    if any(base_key.fullmatch(key) is None or key.endswith('.sql') for key in REVIEWED_BASE):
        raise ValueError('REVIEWED_BASE may hold reviewed core keys only')
    changed = [key for key, digest in old.items() if new.get(key) != digest
               and not (key not in new and NEVER_APPLIED.get(key) == digest)
               and (digest, new.get(key)) not in REVIEWED_BINDINGS.get(key, ())
               and (digest, new.get(key)) not in REVIEWED_MIGRATIONS.get(key, ())
               and REVIEWED_BASE.get(key) != (digest, new.get(key))]
    for key in sorted(changed):
        print('migration immutable: ' + key + ' changed or disappeared; add a new version', file=sys.stderr)
    if changed:
        return 1
    print(f'✓ {len(old)} migration content hashes immutable against {sha[:12]}')
    return 0


if __name__ == '__main__':
    try:
        sys.exit(main())
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        detail = error.stderr if isinstance(error, subprocess.CalledProcessError) else str(error)
        if isinstance(detail, bytes):
            detail = detail.decode(errors='replace')
        print('migration hashes: UNVERIFIED: ' + detail, file=sys.stderr)
        sys.exit(2)
