# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# build-bin.sh — SOURCE THIS instead of writing your own `go build -o bin/olivares`.
# Not executable, not a program: it exists to be `.`-sourced and it defines ONE
# function, build_olivares_bin, which is the single definition of how the engine
# binary is compiled outside a release.
#
# THE DEFECT IT CLOSES, measured 2026-08-09 by on a real first-run debug.
#
#   FIVE scripts wrote the CANONICAL artifact path $ROOT/bin/olivares, and not one
#   of them used the flags Taskfile.yml's build:bin uses. They are not test rigs
#   building into a scratch directory — they overwrite the same file `task build`
#   produces, and the divergence OUTLIVES the script:
#
#     scripts/web-e2e.sh          go build -o "$BIN"                    (no CGO_ENABLED, no -trimpath, no stamp)
#     scripts/web-e2e-demo.sh     go build -o "$BIN"                    (idem)
#     scripts/docs-captures.sh    go build -o "$BIN"                    (idem)
#     scripts/smoke-agentops.sh   CGO_ENABLED=0 go build -trimpath ...  (no stamp)
#     scripts/quickstart-smoke.sh CGO_ENABLED=0 go build -trimpath ...  (no stamp)
#
#   Measured on this box, same source, same commit, the two binaries side by side:
#
#     | absolute paths embedded | task build:bin | the three -trimpath-less scripts |
#     |-------------------------|----------------|----------------------------------|
#     | the checkout directory  | 0              | 1507                             |
#     | the builder's $HOME     | 0              | 2236                             |
#
#   (The two paths are named by ROLE and not spelled out on purpose: this file is
#   part of the public export, and `task lint:export` rejects a checkout path in
#   it — which is the same class of leakage the defect itself is about.)
#
#   That is exactly what the comment on Taskfile.yml's build:bin says -trimpath is
#   there to prevent ("no $HOME/build-host leakage"), reintroduced by running an
#   ordinary `task e2e:web`. And all FIVE dropped the -X stamps, so afterwards
#   `olivares version` answers "dev (commit none, built unknown)" for a working
#   tree that can name its own commit.
#
#   IT IS NOT THE SCRIPTS' FAULT AND THAT IS THE POINT. The flag set was written
#   out longhand in six places, so six places could drift, and five had. A sixth
#   caller tomorrow would drift the same way. Hence one definition, sourced —
#   and scripts/test-build-bin-parity.sh, which fails if a raw `go build` ever
#   writes bin/olivares again.
#
# WHY THE STAMP IS NOT A RELEASE CLAIM. `git describe --tags --always --dirty` on
# a tagless working tree yields a bare commit hash, which is NOT a semantic
# version — so `olivares security check` still answers exit 8 ("cannot determine")
# on it, exactly as it does for an unstamped build. That is correct and intended:
# only a released binary carries a version with a position in the release
# ordering (cmd/olivares/cmd_security.go:104-133, core/release/version.go:58-61).
# The stamp buys provenance — which commit is this? — not a version claim.
#
# NOT the reproducible/release flag set: that is Taskfile.yml's build:repro, which
# pins the date to the commit timestamp, strips the build id and takes the release
# key anchors. Do not fold the two together.

# build_olivares_bin <output-path>
#
# Compiles ./cmd/olivares with the canonical non-release flags. It derives the
# repository root from this file's own location, so it does not depend on the
# caller's cwd.
#
# EXACTLY ONE ARGUMENT, AND THAT IS THE POINT. The first version accepted
# `[extra go build args...]` and appended them AFTER the canonical flags, which
# meant a future caller could pass `-trimpath=false -ldflags=` and turn the one
# definition off from the outside — while the parity ratchet stayed green,
# because G-1 only sees a call to this helper and G-2 only exercises the
# no-extras form. A helper whose whole purpose is "the flag set lives in one
# place" must not take flag overrides. Nobody used it; it could only ever have
# undone the invariant. Found by the the model contrast, 2026-08-09.
build_olivares_bin() {
  if [ "$#" -ne 1 ]; then
    echo "build_olivares_bin: exactly one argument (the output path) is required; got $#" >&2
    echo "  it takes no extra go build flags on purpose — see the note above this function." >&2
    return 2
  fi
  _obb_out="$1"
  # ../.. and not ..: BASH_SOURCE[0] inside a function names the file the FUNCTION
  # is defined in — this one, at scripts/lib/ — never the caller. Getting that
  # wrong resolved the root to scripts/ and the build died with "directory not
  # found", which is a loud failure and therefore the good case; the quiet one
  # would have been a root that happened to exist.
  _obb_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/../.." && pwd)"
  # ASK GIT INSIDE A SUBSHELL THAT HAS NO AMBIENT GIT ENVIRONMENT.
  #
  # GIT_DIR OUTRANKS `-C`. Under an exported GIT_DIR — which git sets for every
  # hook invoked from a LINKED WORKTREE, and every parallel session here works in
  # one — `git -C "$_obb_root" describe` answers about a DIFFERENT repository, so
  # the binary would be stamped with somebody else's commit. A wrong stamp is
  # worse than no stamp: it is provenance that lies, and nothing downstream can
  # tell. Raised by the the model contrast against the first version of this
  # file, which used a bare `git -C`.
  #
  # Sourcing lib/git-env.sh IS the isolation (the file unsets on source, by
  # design), and doing it inside `$( )` keeps that unset in the subshell — the
  # caller's environment is left exactly as it was, which matters because five
  # scripts and build:bin source this helper for a build and did not ask to have
  # their git environment rewritten.
  #
  # `|| echo` on each: a source tarball with no .git is a supported way to build,
  # and it must degrade to the documented default rather than abort the build.
  _obb_gitenv="$(dirname -- "${BASH_SOURCE[0]:-$0}")/git-env.sh"
  _obb_version="$( . "$_obb_gitenv" 2>/dev/null; git -C "$_obb_root" describe --tags --always --dirty 2>/dev/null || echo dev)"
  _obb_commit="$( . "$_obb_gitenv" 2>/dev/null; git -C "$_obb_root" rev-parse --short HEAD 2>/dev/null || echo none)"
  _obb_date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  # CGO_ENABLED=0 (modernc.org/sqlite is pure Go) → fully static, memory-safe.
  # -trimpath strips local paths from the binary (no $HOME/build-host leakage).
  ( cd "$_obb_root" && CGO_ENABLED=0 go build -trimpath \
      -ldflags "-X main.version=${_obb_version} -X main.commit=${_obb_commit} -X main.date=${_obb_date}" \
      -o "$_obb_out" ./cmd/olivares )
}
