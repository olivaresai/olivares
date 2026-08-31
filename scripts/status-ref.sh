#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# status-ref.sh — the ONE owner of live-status publication and reading (D-4-status).
#
# WHY THIS EXISTS. The status protocol used to publish lane boards straight to `main`
# with OLIVARES_FAST_PUSH; the branch-aware gate split closed that escape on main, and
# the interim replacement taught `--no-verify` — the exact habit a gate exists to remove.
# The contrasted design (an internal design note (not shipped)) moves the
# live coordination stream to ONE exact ref, `refs/status/live`, which the pre-push
# classifier sends down the fast lane: normal push, normal hook, no bypass, and one
# global tip whose fast-forward rule arbitrates competing claims.
#
# WHAT THIS SCRIPT GUARANTEES, and how each promise is enforced:
#   * The canonical tree contains ONLY `an internal design note (not shipped)<LANE>.md` blobs (mode 100644)
#     plus the hub board `SESIONES-ACTIVAS.md` — enumerated with NUL records and checked
#     entry by entry; a symlink, an executable, a gitlink, a nested path or a stray file
#     is a named refusal, never a warning.
#   * A routine publication changes EXACTLY ONE lane blob relative to its parent, proved
#     with rename detection OFF and three-answer handling of git's exit status: 0 means
#     "no change" (refused — an empty publish is a mistake), 1 means "changed" (the only
#     acceptance path), anything else means "could not look" (refused: not knowing is
#     not the same as being clean).
#   * Every newly reachable commit is checked back to the previously validated tracking
#     tip or the bootstrap root. Each has zero/one parent, a complete valid status tree,
#     and (after the root) exactly one valid changed path; a good child cannot hide a
#     hostile parent.
#   * The new commit's SOLE parent is the exact remote tip read twice (ls-remote and a
#     real fetch, compared byte for byte). The push is a plain fast-forward: no --force,
#     no --force-with-lease, no --no-verify, no environment escape. Losing the race to
#     another publisher is REPORTED, never auto-retried.
#   * Git trusts ONLY the repository-local/worktree configuration. System/global files are
#     disabled for every Git process (including hooks), so HOME/XDG_CONFIG_HOME cannot select
#     configuration; every explicit repository/object/identity/config-file selector is rejected
#     before the first Git command and stripped again. Publication resolves one explicit,
#     credential-independent transport for `origin`: SSH is preferred when it exactly matches
#     an HTTPS push target; otherwise only an SSH/local target is usable. The private index is
#     per-command only.
#   * GIT_SSH_COMMAND is deliberately inherited. In the three sanctioned containers the
#     entrypoint fixes it and its private key is mounted read-only, so it is a named trusted
#     transport input rather than an accidental omission. Whoever can replace that environment
#     value is already inside the local/container trust boundary and can execute commands as the
#     helper's user.
#     Publish and bootstrap derive the one sanctioned identity from the non-empty user.name
#     and user.email in that trusted local/worktree configuration. There is no fallback. The
#     built commit is then reread and its complete author and committer identities must match
#     those configured values exactly; another genuine GitHub noreply account is not equivalent.
#   * Every blob it will publish is staged in a throwaway detached worktree of fresh
#     origin/main; its post-add index OID and mode are compared with the live entry, so
#     clean filters cannot make the hook inspect different bytes.
#
# LIMITS, declared: this script does not verify the SEMANTIC truth of a status document
# (a 256 KiB ceiling bounds payloads, not honesty), and `project` copies blobs into a
# main checkout — the ordinary full main gate still owns that publication. A command validates
# at most MAX_HISTORY_COMMITS_PER_RUN commits and refuses without advancing its tracking anchor
# if it cannot reach the prior trusted tip or bootstrap root within that budget.
set -uo pipefail

LIVE_REF="refs/status/live"
TRACKING_REF="refs/remotes/origin-status/live"
STATUS_REMOTE_NAME="origin"
BOARD_FILE="SESIONES-ACTIVAS.md"
LANE_DIR="sessions/status"
MAX_BLOB=262144
MAX_HISTORY_COMMITS_PER_RUN=512
LANE_RE='^[A-Za-z0-9][A-Za-z0-9-]{0,63}$'

die() {
	echo "status-ref: $*" >&2
	exit 1
}

# TRUSTED_GIT_CONFIG_BOUNDARY — the single definition of what Git may trust here:
#   * trusted configuration: repository-local config plus config.worktree;
#   * excluded configuration: system and global files, fixed to /dev/null + NOSYSTEM below;
#     this also makes HOME and XDG_CONFIG_HOME irrelevant as Git config-file selectors;
#   * rejected and stripped inputs: every explicit repository/object/identity/config selector
#     documented by git(1)/git-config(1), including historical GIT_CONFIG (used by `git config`)
#     and the indexed command-scope protocol.
# The rejection runs before the first Git command. Discover every KEY_i/VALUE_i name instead of
# guessing a maximum index; the same array drives the per-process defense in depth.
GIT_TRUST_BOUNDARY_REJECTED_ENV=(
	GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_OBJECT_DIRECTORY GIT_COMMON_DIR
	GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_AUTHOR_EMAIL GIT_AUTHOR_NAME
	GIT_COMMITTER_EMAIL GIT_COMMITTER_NAME GIT_CONFIG GIT_CONFIG_GLOBAL
	GIT_CONFIG_SYSTEM GIT_CONFIG_NOSYSTEM GIT_CONFIG_COUNT GIT_CONFIG_PARAMETERS
)
while IFS= read -r v; do
	[[ "$v" =~ ^GIT_CONFIG_(KEY|VALUE)_[0-9]+$ ]] &&
		GIT_TRUST_BOUNDARY_REJECTED_ENV+=("$v")
done < <(compgen -A variable)
for v in "${GIT_TRUST_BOUNDARY_REJECTED_ENV[@]}"; do
	if [[ -v "$v" ]]; then
		die "refusing inherited Git selector $v before repository discovery; run from a clean
status-ref: shell (env -u $v ...). The trusted Git config boundary is local/worktree only."
	fi
done

GIT_TRUST_BOUNDARY_ENV=()
for v in "${GIT_TRUST_BOUNDARY_REJECTED_ENV[@]}"; do
	GIT_TRUST_BOUNDARY_ENV+=(-u "$v")
done
GIT_TRUST_BOUNDARY_ENV+=(
	GIT_CONFIG_GLOBAL=/dev/null
	GIT_CONFIG_SYSTEM=/dev/null
	GIT_CONFIG_NOSYSTEM=1
)
trusted_git() { env "${GIT_TRUST_BOUNDARY_ENV[@]}" git "$@"; }

ROOT="$(trusted_git rev-parse --show-toplevel 2>/dev/null)" || die "not inside a git repository"
g() { trusted_git -C "$ROOT" "$@"; }
indexed_g() { # <private-index> <git-args...>; the only sanctioned selector reintroduction
	local index="$1"
	shift
	env "${GIT_TRUST_BOUNDARY_ENV[@]}" GIT_INDEX_FILE="$index" git -C "$ROOT" "$@"
}

# remote_endpoint <ssh-or-https-url> — reduce only the narrow URL forms this helper can
# compare without guessing: HTTPS, ssh://, and scp-like SSH. User names and numeric transport
# ports do not identify a repository; host (case-folded) plus path do. Query/fragment-bearing,
# credential-bearing HTTPS, IPv6, or otherwise ambiguous forms are deliberately unsupported.
remote_endpoint() {
	local url="$1" rest authority host path port
	case "$url" in
	https://*)
		rest="${url#https://}"
		authority="${rest%%/*}"
		[ "$authority" != "$rest" ] && [ -n "$authority" ] || return 1
		case "$authority" in *@*|\[*\]*) return 1 ;; esac
		host="$authority"
		case "$host" in
		*:*)
			port="${host##*:}"
			[[ "$port" =~ ^[0-9]+$ ]] || return 1
			host="${host%:*}"
			;;
		esac
		path="${rest#*/}"
		;;
	ssh://*)
		rest="${url#ssh://}"
		authority="${rest%%/*}"
		[ "$authority" != "$rest" ] && [ -n "$authority" ] || return 1
		host="${authority##*@}"
		case "$host" in \[*\]*) return 1 ;; esac
		case "$host" in
		*:*)
			port="${host##*:}"
			[[ "$port" =~ ^[0-9]+$ ]] || return 1
			host="${host%:*}"
			;;
		esac
		path="${rest#*/}"
		;;
	*:*)
		authority="${url%%:*}"
		path="${url#*:}"
		case "$authority" in ""|*/*) return 1 ;; esac
		host="${authority##*@}"
		;;
	*) return 1 ;;
	esac
	[ -n "$host" ] && [ -n "$path" ] || return 1
	case "$host$path" in *$'\n'*|*$'\r'*|*$'\t'*|*\?*|*\#*) return 1 ;; esac
	printf '%s/%s' "${host,,}" "$path"
}

remote_url_kind() {
	local url="$1"
	case "$url" in
	""|*$'\n'*|*$'\r'*|*$'\t'*) printf 'unsupported' ;;
	ssh://*) remote_endpoint "$url" >/dev/null && printf 'ssh' || printf 'unsupported' ;;
	https://*) remote_endpoint "$url" >/dev/null && printf 'https' || printf 'unsupported' ;;
	file://?*|/*|./*|../*) printf 'local' ;;
	*://*|*::* ) printf 'unsupported' ;;
	*:*) remote_endpoint "$url" >/dev/null && printf 'ssh' || printf 'unsupported' ;;
	*) printf 'local' ;;
	esac
}

# select_status_remote_url — choose one transport before any status object is built. Git's
# normal `push origin` prefers remote.origin.pushurl, which is HTTPS in these containers and
# therefore loses the global credential helper intentionally excluded above. When that HTTPS
# target has one SSH fetch URL for the exact same host/path, use the SSH URL explicitly for
# ls-remote/fetch/push. Local paths remain usable for hermetic fixtures. Anything else fails by
# remote name instead of discovering missing credentials after the work is complete.
select_status_remote_url() {
	local fetch_file="$WORK/remote-fetch-urls" push_file="$WORK/remote-push-urls"
	local rc url target target_kind target_endpoint candidate_endpoint chosen="" matches=0
	local -a fetch_urls=() push_urls=()

	g config --null --get-all "remote.$STATUS_REMOTE_NAME.url" >"$fetch_file"
	rc=$?
	[ "$rc" -eq 0 ] || {
		[ "$rc" -eq 1 ] &&
			die "remote '$STATUS_REMOTE_NAME' has no configured URL"
		die "could not read remote '$STATUS_REMOTE_NAME' URLs (git config rc=$rc)"
	}
	while IFS= read -r -d '' url; do fetch_urls+=("$url"); done <"$fetch_file"
	[ "${#fetch_urls[@]}" -gt 0 ] || die "remote '$STATUS_REMOTE_NAME' has no configured URL"

	g config --null --get-all "remote.$STATUS_REMOTE_NAME.pushurl" >"$push_file"
	rc=$?
	if [ "$rc" -eq 0 ]; then
		while IFS= read -r -d '' url; do push_urls+=("$url"); done <"$push_file"
	elif [ "$rc" -ne 1 ]; then
		die "could not read remote '$STATUS_REMOTE_NAME' push URLs (git config rc=$rc)"
	fi
	[ "${#push_urls[@]}" -le 1 ] ||
		die "remote '$STATUS_REMOTE_NAME' has ${#push_urls[@]} push URLs; the live ref requires exactly one destination"

	if [ "${#push_urls[@]}" -eq 1 ]; then target="${push_urls[0]}"; else target="${fetch_urls[0]}"; fi
	target_kind="$(remote_url_kind "$target")"
	case "$target_kind" in
	ssh|local)
		printf '%s' "$target"
		return 0
		;;
	https)
		target_endpoint="$(remote_endpoint "$target")" ||
			die "remote '$STATUS_REMOTE_NAME' has an ambiguous HTTPS push target"
		for url in "${fetch_urls[@]}"; do
			[ "$(remote_url_kind "$url")" = "ssh" ] || continue
			candidate_endpoint="$(remote_endpoint "$url")" || continue
			if [ "$candidate_endpoint" = "$target_endpoint" ]; then
				chosen="$url"
				matches=$((matches + 1))
			fi
		done
		[ "$matches" -eq 1 ] || {
			[ "$matches" -eq 0 ] && die "remote '$STATUS_REMOTE_NAME' has only an HTTPS publication target and no exactly matching SSH URL; global credential helpers remain disabled"
			die "remote '$STATUS_REMOTE_NAME' has $matches matching SSH URLs; refusing an ambiguous publication transport"
		}
		printf '%s' "$chosen"
		;;
	*)
		die "remote '$STATUS_REMOTE_NAME' has no usable SSH or local publication transport"
		;;
	esac
}

# Named publication wrapper: the same boundary must reach Git's pre-push hook and transport.
# Keeping both push sites behind it makes a later raw-Git refactor mutation-testable.
clean_git() {
	local repo="$1"
	shift
	trusted_git -C "$repo" "$@"
}

WORK="$(mktemp -d "${TMPDIR:-/tmp}/olivares-status-ref.XXXXXX")" || die "mktemp failed"
cleanup() {
	if [ -n "${PROJ_WT:-}" ]; then
		trusted_git -C "$ROOT" worktree remove --force "$PROJ_WT" >/dev/null 2>&1 || true
	fi
	rm -rf "$WORK"
}
trap cleanup EXIT HUP INT TERM

is_hex_oid() { case "$1" in *[!0-9a-f]*) return 1 ;; esac; [ "${#1}" -eq 40 ] || [ "${#1}" -eq 64 ]; }

is_live_path() {
	local path="$1" leaf lane
	[ "$path" = "$BOARD_FILE" ] && return 0
	case "$path" in
	"$LANE_DIR"/*.md)
		leaf="${path#"$LANE_DIR"/}"
		case "$leaf" in */*) return 1 ;; esac
		lane="${leaf%.md}"
		[ "$leaf" = "$lane.md" ] || return 1
		[[ "$lane" =~ $LANE_RE ]] && [ "$lane" != "README" ]
		;;
	*) return 1 ;;
	esac
}

# remote_tip -> prints the live OID, or nothing if the ref does not exist. A transport
# failure is a REFUSAL, not an empty answer: "could not look" must never read as absent.
remote_tip() {
	local out rc
	out="$(g ls-remote --refs "$STATUS_REMOTE_URL" "$LIVE_REF" 2>"$WORK/lsr.err")"
	rc=$?
	[ "$rc" -eq 0 ] || die "ls-remote failed (rc=$rc): $(head -1 "$WORK/lsr.err" 2>/dev/null)"
	[ -n "$out" ] || return 0
	local oid="${out%%$'\t'*}"
	is_hex_oid "$oid" || die "remote advertised a non-hex tip '$oid' for $LIVE_REF"
	printf '%s' "$oid"
}

# fetch_tip <oid> — fetch the exact advertised tip into a unique local ref and require
# the fetched commit to BE that oid. The advertised object need not exist locally (a
# stale clone reads the OID but cannot open it — measured in the design audit).
fetch_tip() {
	local oid="$1" tmpref="refs/olivares-status-tmp/$$"
	g fetch -q --no-tags "$STATUS_REMOTE_URL" "+$LIVE_REF:$tmpref" 2>"$WORK/fetch.err" ||
		die "fetch of $LIVE_REF failed: $(head -1 "$WORK/fetch.err" 2>/dev/null)"
	local got
	got="$(g rev-parse "$tmpref^{commit}" 2>/dev/null)" || die "fetched ref is not a commit"
	g update-ref -d "$tmpref" 2>/dev/null || true
	[ "$got" = "$oid" ] ||
		die "advertised tip $oid and fetched tip $got disagree — refusing to build on either"
	printf '%s' "$got"
}

# validate_tree <commit> — enumerate the WHOLE tree with NUL records; every entry must be
# a 100644 blob at an allowed path, no larger than MAX_BLOB. Prints nothing; dies loudly.
validate_tree() {
	local commit="$1" mode type oid path n=0 lanes_seen=" "
	local lst="$WORK/lstree.$$"
	if ! g ls-tree -r -t -z "$commit" >"$lst" 2>"$WORK/lstree.err"; then
		die "could not enumerate the tree at $commit: $(head -1 "$WORK/lstree.err" 2>/dev/null) — refusing a partial listing"
	fi
	local rec
	while IFS= read -r -d '' rec; do
		mode="${rec%% *}"; rec="${rec#* }"
		type="${rec%% *}"; rec="${rec#* }"
		oid="${rec%%$'\t'*}"
		path="${rec#*$'\t'}"
		if [ "$type" = "tree" ]; then
			case "$path" in
			sessions | sessions/status) continue ;;
			*) die "unexpected subtree '$path' in the status tree — refused (even empty)" ;;
			esac
		fi
		n=$((n + 1))
		[ "$type" = "blob" ] || die "tree entry '$path' is a $type, not a blob — refused"
		[ "$mode" = "100644" ] || die "tree entry '$path' has mode $mode (want 100644) — refused"
		case "$path" in
		"$BOARD_FILE") : ;;
		"$LANE_DIR"/*.md)
			is_live_path "$path" || die "lane file '$path' violates the exact lane-path rule"
			local lane="${path#"$LANE_DIR"/}"
			lane="${lane%.md}"
			# Case-folding here decides whether two lanes collide, so it must not depend
			# on a process whose failure nobody reads: `lc="$(... | tr ...)"` yields an
			# EMPTY key when tr dies, and an empty key collides with nothing. Bash's own
			# expansion cannot fail halfway.
			local lc="${lane,,}"
			[ -n "$lc" ] || die "could not case-fold lane name '$lane' — refused"
			case "$lanes_seen" in
			*" $lc "*) die "case-fold lane collision on '$lane' — two lanes differing only in case" ;;
			esac
			lanes_seen="$lanes_seen$lc "
			;;
		*) die "unexpected path '$path' in the status tree — refused" ;;
		esac
		local size
		size="$(g cat-file -s "$oid" 2>/dev/null)" || die "cannot size blob for '$path'"
		[ "$size" -le "$MAX_BLOB" ] || die "'$path' is $size bytes (ceiling $MAX_BLOB) — refused"
	done <"$lst"
	rm -f "$lst"
	[ "$n" -gt 0 ] || die "the status tree at $commit is empty — refused"
}

# validate_status_commit <commit> — validate one history edge and leave its sole parent in
# VALIDATED_PARENT (empty only for the bootstrap root).
validate_status_commit() {
	local commit="$1"
	local parents
	parents="$(g rev-list --parents -n1 "$commit" 2>/dev/null)" || die "cannot read the tip commit"
	# Counting parents decides whether history validation continues or stops at a "root",
	# so it must not run through a process whose failure is invisible: with `wc` dead the
	# arithmetic became $(( - 1)) = -1, which is <= 1 and not == 1, so a two-parent
	# ancestor was silently treated as the root and its history never checked. Split with
	# the shell itself, and refuse a record that does not even name the commit.
	local -a parent_words=()
	read -r -a parent_words <<<"$parents" || die "unreadable parent record for $commit"
	[ "${#parent_words[@]}" -ge 1 ] || die "empty parent record for $commit"
	local pcount=$(( ${#parent_words[@]} - 1 ))
	[ "$pcount" -le 1 ] ||
		die "status commit $commit has $pcount parents — a merge is not a status update"
	VALIDATED_PARENT=""
	if [ "$pcount" -eq 1 ]; then
		local parent="${parents#* }"
		local df="$WORK/tipdiff.$$" rc=0
		g diff-tree --no-renames --exit-code --name-only -r -z "$parent" "$commit" >"$df" 2>/dev/null || rc=$?
		[ "$rc" -eq 1 ] || { [ "$rc" -eq 0 ] && die "the live tip changes nothing — refused"; } ||
			die "could not diff the tip against its parent (rc=$rc)"
		local count=0 pathrec
		while IFS= read -r -d '' pathrec; do
			count=$((count + 1))
			is_live_path "$pathrec" ||
				die "status commit $commit touches disallowed path '$pathrec' — refused"
		done <"$df"
		rm -f "$df"
		[ "$count" -eq 1 ] ||
			die "status commit $commit touches $count paths (want exactly 1) — refused"
		VALIDATED_PARENT="$parent"
	fi
}

# validate_history <announced-tip> <previously-validated-tip-or-empty> — validate every
# newly reachable commit and full tree back to the trusted tracking tip, or all the way to
# the bootstrap root when this clone has no prior validated tip. A valid child cannot hide a
# merge, a two-lane update, a hostile tree, or an unreadable intermediate object.
validate_history() {
	local current="$1" known="$2" checked=0
	while :; do
		checked=$((checked + 1))
		[ "$checked" -le "$MAX_HISTORY_COMMITS_PER_RUN" ] ||
			die "history exceeds MAX_HISTORY_COMMITS_PER_RUN=$MAX_HISTORY_COMMITS_PER_RUN before
status-ref: reaching its trusted anchor/root — refusing without advancing the tracking ref"
		validate_tree "$current"
		validate_status_commit "$current"
		if [ -n "$known" ] && [ "$current" = "$known" ]; then
			return 0
		fi
		if [ -z "$VALIDATED_PARENT" ]; then
			[ -z "$known" ] ||
				die "previously validated tip $known is not reachable from announced tip $1"
			return 0
		fi
		current="$VALIDATED_PARENT"
	done
}

known_validated_tip() {
	local raw rc=0
	raw="$(g rev-parse --verify -q "$TRACKING_REF" 2>/dev/null)" || rc=$?
	if [ "$rc" -ne 0 ]; then
		[ "$rc" -eq 1 ] || die "could not inspect prior validated tip $TRACKING_REF"
		return 0
	fi
	[ "$(g cat-file -t "$raw" 2>/dev/null)" = "commit" ] ||
		die "prior validated tip $TRACKING_REF is not a commit"
	printf '%s' "$raw"
}

lane_path_for() {
	local lane="$1"
	[[ "$lane" =~ $LANE_RE ]] || die "lane name '$lane' violates ^[A-Za-z0-9][A-Za-z0-9-]{0,63}\$"
	[ "$lane" != "README" ] || die "README is reserved"
	printf '%s/%s.md' "$LANE_DIR" "$lane"
}

# stage_in_main_projection <path> <blobOID> — build a FRESH detached worktree of
# origin/main (fetched first, so it is today's main and not a stale memory), materialize
# and STAGE the blob there, and byte-compare it back. The worktree is kept alive: the
# push itself runs FROM it, so the pre-push fast lints judge the projection the file
# will take in main — not whatever the caller's checkout happens to contain (round-1
# finding I-01).
# open_main_projection — create the private origin/main worktree the hook will judge.
# Split out of stage_in_main_projection so bootstrap can stage N paths into ONE
# projection: it publishes a whole manifest, and calling the combined helper per lane
# would try to create the worktree again and fail on the second lane.
open_main_projection() {
	g fetch -q --no-tags "$STATUS_REMOTE_URL" \
		"+refs/heads/main:refs/remotes/origin/main" 2>"$WORK/fm.err" ||
		die "could not fetch origin/main for the projection: $(head -1 "$WORK/fm.err")"
	PROJ_WT="$WORK/mainwt"
	g worktree add --detach -q "$PROJ_WT" origin/main 2>"$WORK/wt.err" ||
		die "could not create the projection worktree: $(head -1 "$WORK/wt.err")"
}

# stage_one_in_projection <path> <blob> — materialise EXACTLY the published bytes into the
# projection and prove, after `git add`, that the index carries that same OID and mode.
stage_one_in_projection() {
	local path="$1" blob="$2"
	mkdir -p "$PROJ_WT/$(dirname "$path")" ||
		die "could not create the projection directory for '$path'"
	g cat-file blob "$blob" >"$PROJ_WT/$path" || die "could not materialize $path"
	local back
	back="$(trusted_git -C "$PROJ_WT" hash-object -- "$path")" ||
		die "hash-object failed in projection"
	[ "$back" = "$blob" ] || die "projection blob $back != live blob $blob — refused"
	trusted_git -C "$PROJ_WT" add -- "$path" || die "could not stage the projection"
	verify_staged_entry "$PROJ_WT" "$path" "100644" "$blob"
}

stage_in_main_projection() {
	open_main_projection
	stage_one_in_projection "$1" "$2"
}

# verify_staged_entry <repo> <path> <mode> <oid> — prove what the hook/index will see
# AFTER `git add`, including the clean-filter result and index mode.
verify_staged_entry() {
	local repo="$1" path="$2" expected_mode="$3" expected_oid="$4"
	local staged_oid rec rest mode listed_oid listed_path stage_file
	staged_oid="$(trusted_git -C "$repo" rev-parse ":$path" 2>/dev/null)" ||
		die "could not read staged OID for '$path'"
	[ "$staged_oid" = "$expected_oid" ] ||
		die "staged blob $staged_oid for '$path' != live blob $expected_oid — refused"
	STAGE_CHECK_SEQ=$((STAGE_CHECK_SEQ + 1))
	stage_file="$WORK/staged.$STAGE_CHECK_SEQ"
	trusted_git -C "$repo" ls-files --stage -z -- "$path" >"$stage_file" ||
		die "could not read staged mode for '$path'"
	IFS= read -r -d '' rec <"$stage_file" || die "no staged entry for '$path'"
	mode="${rec%% *}"; rest="${rec#* }"
	listed_oid="${rest%% *}"; rest="${rest#* }"; listed_path="${rest#*$'\t'}"
	[ "$listed_path" = "$path" ] || die "staged path '$listed_path' != expected '$path'"
	[ "$listed_oid" = "$staged_oid" ] || die "staged OID readers disagree for '$path'"
	[ "$mode" = "$expected_mode" ] ||
		die "staged mode $mode for '$path' != live mode $expected_mode — refused"
}
STAGE_CHECK_SEQ=0

require_sanctioned_identity() {
	SANCTIONED_NAME="$(g config --get user.name 2>/dev/null)" ||
		die "no sanctioned identity configured in local/worktree Git config: user.name is missing"
	[ -n "$SANCTIONED_NAME" ] ||
		die "no sanctioned identity configured in local/worktree Git config: user.name is empty"
	SANCTIONED_EMAIL="$(g config --get user.email 2>/dev/null)" ||
		die "no sanctioned identity configured in local/worktree Git config: user.email is missing"
	[ -n "$SANCTIONED_EMAIL" ] ||
		die "no sanctioned identity configured in local/worktree Git config: user.email is empty"
}

identity_matches_sanctioned() { # <name> <email>; exact strings, never a domain class
	[ "$1" = "$SANCTIONED_NAME" ] && [ "$2" = "$SANCTIONED_EMAIL" ]
}

verify_built_identity() { # <commit>
	local commit="$1" a_name a_email c_name c_email
	a_name="$(g show -s --format=%an "$commit")" || die "could not read built author name"
	a_email="$(g show -s --format=%ae "$commit")" || die "could not read built author email"
	c_name="$(g show -s --format=%cn "$commit")" || die "could not read built committer name"
	c_email="$(g show -s --format=%ce "$commit")" || die "could not read built committer email"
	identity_matches_sanctioned "$a_name" "$a_email" ||
		die "built author identity does not exactly match the sanctioned repository identity"
	identity_matches_sanctioned "$c_name" "$c_email" ||
		die "built committer identity does not exactly match the sanctioned repository identity"
}

do_publish() {
	local board=0
	if [ "$1" = "--board" ]; then board=1; shift; fi
	local lane="" draft path
	if [ "$board" -eq 1 ]; then
		draft="${1:?usage: status-ref.sh publish-board <draft-file>}"
		path="$BOARD_FILE"
		lane="(board)"
	else
		lane="${1:?usage: status-ref.sh publish <LANE> <draft-file>}"
		draft="${2:?usage: status-ref.sh publish <LANE> <draft-file>}"
		path="$(lane_path_for "$lane")"
	fi
	[ -f "$draft" ] || die "draft file '$draft' does not exist"
	# Snapshot the draft into the object store BEFORE measuring it, and measure the OID —
	# not the pathname. Sizing `$draft` here and re-reading `$draft` at blob time left a
	# window in which the file changed between the check and the publication, and nothing
	# downstream ever looked at the size of the bytes actually published: a draft grown
	# past the ceiling after the precheck was published with rc=0 and broke every later
	# read. Sizing through `wc -c` also threw away its exit status, so a `wc` that printed
	# a small number and died passed the same gate.
	local blob dsize
	blob="$(g hash-object -w -- "$draft")" || die "hash-object of the draft failed"
	dsize="$(g cat-file -s "$blob")" || die "could not size the hashed draft blob $blob"
	[ "$dsize" -le "$MAX_BLOB" ] || die "draft is $dsize bytes (ceiling $MAX_BLOB) — refused"
	[ "$dsize" -gt 0 ] || die "draft is empty — an empty status is a mistake, not a message"

	local tip known
	known="$(known_validated_tip)" || exit $?
	tip="$(remote_tip)" || exit $?
	[ -n "$tip" ] || die "$LIVE_REF does not exist on origin — run 'bootstrap' first (hub only)"
	fetch_tip "$tip" >/dev/null
	validate_history "$tip" "$known"

	# TEST SEAM, deliberately narrow: an integer pause (0-10s) between reading the tip and
	# pushing, so the battery can exercise the LOSING side of the publication race with a
	# real concurrent writer. Numeric-only — an env var must never be a code path (the
	# pg-env probe learned that the hard way).
	case "${STATUS_REF_TEST_SLEEP_AFTER_READ:-0}" in
	0) : ;;
	[1-9]|10) sleep "$STATUS_REF_TEST_SLEEP_AFTER_READ" ;;
	*) die "STATUS_REF_TEST_SLEEP_AFTER_READ accepts an integer 0-10, got '$STATUS_REF_TEST_SLEEP_AFTER_READ'" ;;
	esac

	local idx="$WORK/index"
	indexed_g "$idx" read-tree "$tip" || die "read-tree of the live tip failed"
	# $blob is the snapshot hashed above, not a fresh read of the pathname.
	indexed_g "$idx" update-index --add --cacheinfo "100644,$blob,$path" ||
		die "update-index failed"
	local tree
	tree="$(indexed_g "$idx" write-tree)" || die "write-tree failed"

	# Prove the proposed tree differs from its parent at EXACTLY the derived path, with
	# rename detection off. Three answers, three outcomes — and the NUL stream goes to a
	# FILE: a command substitution silently EATS NUL bytes (measured in the round-1
	# contrast), so a multi-path delta could masquerade as one concatenated name.
	local df="$WORK/pubdiff.$$" rc=0
	g diff-tree --no-renames --exit-code --name-only -r -z "$tip" "$tree" >"$df" 2>"$WORK/dt.err" || rc=$?
	if [ "$rc" -eq 0 ]; then
		# "Identical to the live copy" is a claim about the REMOTE, and $tip was read
		# before the history walk. If the ref moved since, that sentence is false and the
		# caller would drop a genuine update believing it was redundant. Re-ask first.
		local now_noop
		now_noop="$(remote_tip)" || exit $?
		if [ "$now_noop" != "$tip" ]; then
			echo "status-ref: the live tip moved to ${now_noop:-<absent>} while this update was" >&2
			echo "status-ref: being built on $tip, so it is NOT byte-identical to what is live" >&2
			echo "status-ref: now. Nothing was published. Re-read (status-ref.sh read --all) and" >&2
			echo "status-ref: decide against the state that actually won." >&2
			exit 3
		fi
		die "the draft is byte-identical to the live copy — nothing to publish"
	elif [ "$rc" -ne 1 ]; then
		die "diff-tree could not compare the trees (rc=$rc) — refusing on an unknown answer"
	fi
	local count=0 only="" pathrec
	while IFS= read -r -d '' pathrec; do
		count=$((count + 1))
		only="$pathrec"
	done <"$df"
	rm -f "$df"
	[ "$count" -eq 1 ] || die "the proposed commit changes $count paths (want exactly 1) — refused"
	[ "$only" = "$path" ] || die "the proposed commit changes '$only', not '$path' — refused"

	stage_in_main_projection "$path" "$blob"

	local trailer="Status-Lane: $lane"
	[ "$board" -eq 1 ] && trailer="Status-Board: hub"
	require_sanctioned_identity
	local msg="docs(status): live update for ${lane}

${trailer}
Signed-off-by: ${SANCTIONED_NAME} <${SANCTIONED_EMAIL}>"
	local newc
	newc="$(printf '%s\n' "$msg" | g commit-tree "$tree" -p "$tip")" || die "commit-tree failed"

	# Re-read what we built before showing it to the remote.
	local parent_line built_parent built_tree
	parent_line="$(g rev-list --parents -n1 "$newc")" ||
		die "could not re-read the built commit's parents"
	local -a parent_words=()
	read -r -a parent_words <<<"$parent_line" || die "built commit has unreadable parent metadata"
	[ "${#parent_words[@]}" -eq 2 ] ||
		die "built commit has $((${#parent_words[@]} - 1)) parents (want exactly 1)"
	built_parent="$(g rev-parse "$newc^")" || die "could not re-read the built commit's parent"
	[ "$built_parent" = "$tip" ] || die "built commit's parent is not the live tip"
	built_tree="$(g rev-parse "$newc^{tree}")" || die "could not re-read the built commit's tree"
	[ "$built_tree" = "$tree" ] || die "built commit does not carry the proposed tree"
	verify_built_identity "$newc"

	# Never publish a state this helper's own READER would refuse. Everything above checks
	# the commit's shape; this runs the exact pair the read path runs over live history, so
	# a writer cannot poison the bus and be told it succeeded.
	validate_tree "$newc"
	validate_status_commit "$newc"
	[ "$VALIDATED_PARENT" = "$tip" ] ||
		die "the proposed commit does not validate onto the live tip — refusing to publish"

	# Plain fast-forward push FROM the origin/main projection worktree: the fast lints of
	# the pre-push hook judge the tree this file will take in main (I-01), and the old tip
	# is the lease.
	if ! clean_git "$PROJ_WT" push -q "$STATUS_REMOTE_URL" "$newc:$LIVE_REF" \
		2>"$WORK/push.err"; then
		local now
		now="$(remote_tip)" || exit $?
		# The remote may have ACCEPTED this very commit and lost the answer on the way
		# back. That is a transport failure, not a lost race, and it must never be
		# announced as "Nothing was published": the caller would reapply work that is
		# already live, on top of itself. Check for our own commit before anything else.
		if [ "$now" = "$newc" ]; then
			echo "status-ref: the push reported failure, but the live tip IS $newc — the" >&2
			echo "status-ref: remote accepted this exact commit and the answer was lost in" >&2
			echo "status-ref: transport: $(head -1 "$WORK/push.err" 2>/dev/null)" >&2
			g update-ref "$TRACKING_REF" "$newc" ||
				die "could not record the published tip after a transport failure"
			echo "status-ref: published $path at $newc (parent $tip) — confirmed after a transport failure"
			return 0
		fi
		if [ -n "$now" ] && [ "$now" != "$tip" ]; then
			echo "status-ref: LOST the publication race — the live tip moved to $now while this" >&2
			echo "status-ref: update was built on $tip. Nothing was published. Read the winning" >&2
			echo "status-ref: state (status-ref.sh read --all) and consciously reapply yours;" >&2
			echo "status-ref: an automatic retry would overwrite the decision that just won." >&2
			exit 3
		fi
		die "push of $LIVE_REF failed: $(head -1 "$WORK/push.err" 2>/dev/null)"
	fi
	local final
	final="$(remote_tip)" || exit $?
	[ "$final" = "$newc" ] || die "post-push verification failed: remote is $final, built $newc"
	g update-ref "$TRACKING_REF" "$newc" || die "could not record validated published tip"
	echo "status-ref: published $path at $newc (parent $tip)"
}

do_read() {
	local want="${1:?usage: status-ref.sh read <LANE>|--all}"
	local tip known
	known="$(known_validated_tip)" || exit $?
	tip="$(remote_tip)" || exit $?
	[ -n "$tip" ] || { echo "status-ref: UNKNOWN — $LIVE_REF does not exist on origin" >&2; exit 2; }
	fetch_tip "$tip" >/dev/null
	validate_history "$tip" "$known"
	g update-ref "$TRACKING_REF" "$tip" || die "could not record the fetched tip"
	if [ "$want" = "--all" ]; then
		local stamp listing="$WORK/read-tree"
		stamp="$(g show -s --format=%ci "$tip")" || die "could not read live commit time"
		echo "# live status @ $tip ($stamp)"
		g ls-tree -r -z "$tip" >"$listing" || die "could not enumerate live documents for read"
		local rec mode oid path
		while IFS= read -r -d '' rec; do
			mode="${rec%% *}"; rec="${rec#* }"; rec="${rec#* }"
			oid="${rec%%$'\t'*}"; path="${rec#*$'\t'}"
			echo
			echo "===== $path ====="
			g cat-file blob "$oid" || die "could not read live blob '$path'"
		done <"$listing"
	else
		local path entry
		path="$(lane_path_for "$want")"
		# THREE answers, never two. This branch used to collapse "the lane has no
		# document" and "I could not read the document it HAS" into the same
		# UNKNOWN/exit 2, because any failure of the final cat-file took that arm —
		# a readable tree with an unreadable blob reported absence. Resolve the path
		# against the already-validated tree FIRST, with a primitive that separates
		# them: ls-tree exits 0 with EMPTY output when the path is absent, and
		# non-zero when it could not look at the tree at all. So exit 2 now means
		# "demonstrably absent" and nothing else.
		if ! entry="$(g ls-tree -z --full-tree "$tip" -- "$path")"; then
			die "could not inspect the live tree at $tip for lane '$want'"
		fi
		if [ -z "$entry" ]; then
			echo "status-ref: UNKNOWN — lane '$want' has no live document at $tip" >&2
			exit 2
		fi
		g cat-file blob "$tip:$path" ||
			die "lane '$want' HAS a live document at $tip ($path) but its blob could not be read"
	fi
}

do_project() {
	[ "${1:-}" = "--all" ] || die "usage: status-ref.sh project --all <clean-main-worktree>"
	local dest="${2:?usage: status-ref.sh project --all <clean-main-worktree>}"
	[ -d "$dest/.git" ] || trusted_git -C "$dest" rev-parse --git-dir >/dev/null 2>&1 ||
		die "'$dest' is not a git worktree"
	local status_before="$WORK/project-status.before"
	trusted_git -C "$dest" status --porcelain=v1 -z >"$status_before" \
		2>"$WORK/project-status.err" ||
		die "git status could not inspect the projection worktree"
	[ ! -s "$status_before" ] || die "projection worktree is not clean"
	local branch
	branch="$(trusted_git -C "$dest" symbolic-ref --quiet --short HEAD 2>/dev/null)" ||
		die "projection destination must have main checked out (not detached)"
	[ "$branch" = "main" ] || die "projection destination is branch '$branch' (want exactly main)"
	local source_origin dest_origin
	source_origin="$(g remote get-url origin)" || die "source repository has no readable origin"
	dest_origin="$(trusted_git -C "$dest" remote get-url origin)" ||
		die "projection destination has no readable origin"
	[ "$dest_origin" = "$source_origin" ] ||
		die "projection destination origin '$dest_origin' != source origin '$source_origin'"
	trusted_git -C "$dest" fetch -q --no-tags "$STATUS_REMOTE_URL" \
		"+refs/heads/main:refs/remotes/origin/main" 2>"$WORK/project-fetch.err" ||
		die "could not refresh destination origin/main: $(head -1 "$WORK/project-fetch.err")"
	local head_oid main_oid
	head_oid="$(trusted_git -C "$dest" rev-parse --verify HEAD^{commit})" ||
		die "cannot read destination HEAD"
	main_oid="$(trusted_git -C "$dest" rev-parse --verify refs/remotes/origin/main^{commit})" ||
		die "cannot read freshly fetched destination origin/main"
	[ "$head_oid" = "$main_oid" ] ||
		die "projection main is stale: HEAD $head_oid != fresh origin/main $main_oid"

	local tip known
	known="$(known_validated_tip)" || exit $?
	tip="$(remote_tip)" || exit $?
	[ -n "$tip" ] || die "nothing to project: $LIVE_REF does not exist"
	fetch_tip "$tip" >/dev/null
	validate_history "$tip" "$known"

	# Capture the exact live manifest through a checked NUL stream. The associative maps are
	# safe only after validate_history has constrained every path to the fixed lane grammar.
	local manifest="$WORK/project-manifest"
	g ls-tree -r -z "$tip" >"$manifest" 2>"$WORK/project-ls-tree.err" ||
		die "could not enumerate the exact live manifest"
	local rec mode type oid path rest
	declare -A live_oids=() live_modes=()
	while IFS= read -r -d '' rec; do
		mode="${rec%% *}"; rest="${rec#* }"; type="${rest%% *}"; rest="${rest#* }"
		oid="${rest%%$'\t'*}"; path="${rest#*$'\t'}"
		[ "$mode" = "100644" ] && [ "$type" = "blob" ] && is_live_path "$path" ||
			die "validated manifest changed while projecting '$path'"
		live_oids["$path"]="$oid"
		live_modes["$path"]="$mode"
	done <"$manifest"

	# Delete every tracked lane/board snapshot that is absent from the live manifest. README
	# and any non-snapshot documentation below an internal design note (not shipped) remain ordinary main content.
	local tracked="$WORK/project-tracked"
	trusted_git -C "$dest" ls-files -z -- "$BOARD_FILE" "$LANE_DIR" >"$tracked" ||
		die "could not enumerate tracked status snapshots"
	while IFS= read -r -d '' path; do
		if is_live_path "$path" && [[ ! -v "live_oids[$path]" ]]; then
			trusted_git -C "$dest" rm -q -- "$path" ||
				die "could not remove stale snapshot '$path'"
		fi
	done <"$tracked"

	for path in "${!live_oids[@]}"; do
		mkdir -p "$dest/$(dirname "$path")" || die "could not create directory for '$path'"
		g cat-file blob "${live_oids[$path]}" >"$dest/$path" ||
			die "could not materialize live blob for '$path'"
		trusted_git -C "$dest" add -- "$path" ||
			die "could not stage projected path '$path'"
		verify_staged_entry "$dest" "$path" "${live_modes[$path]}" "${live_oids[$path]}"
	done

	local staged="$WORK/project-staged"
	trusted_git -C "$dest" diff --cached --name-only -z >"$staged" ||
		die "could not enumerate the staged projection diff"
	while IFS= read -r -d '' path; do
		is_live_path "$path" || die "projection staged unexpected path '$path'"
	done <"$staged"
	local unstaged_rc=0
	trusted_git -C "$dest" diff --quiet -- || unstaged_rc=$?
	[ "$unstaged_rc" -eq 0 ] || {
		[ "$unstaged_rc" -eq 1 ] && die "projection left unstaged changes"
		die "could not inspect projection worktree diff (rc=$unstaged_rc)"
	}
	local untracked="$WORK/project-untracked"
	trusted_git -C "$dest" ls-files --others --exclude-standard -z >"$untracked" ||
		die "could not enumerate projection worktree untracked files"
	[ ! -s "$untracked" ] || die "projection created an untracked path"
	trusted_git -C "$dest" status --porcelain=v1 -z >"$WORK/project-status.after" ||
		die "git status could not verify the staged projection"
	# Record the anchor ONLY here, once every projection check has passed. Without this the
	# tip this run just validated is forgotten, so a container that only ever projects
	# re-validates from the root on each run and eventually refuses at the 512-commit
	# budget — a helper that gets slower and then stops, for work it already did.
	g update-ref "$TRACKING_REF" "$tip" || die "could not record the projected validated tip"
	echo "status-ref: projected live tip $tip into $dest (staged; commit and gate as usual)"
}

do_bootstrap() {
	[ $# -ge 1 ] || die "usage: status-ref.sh bootstrap <lane...> (hub only, once)"
	local tip
	tip="$(remote_tip)" || exit $?
	[ -z "$tip" ] || die "$LIVE_REF already exists at $tip — bootstrap runs exactly once"
	require_sanctioned_identity
	local idx="$WORK/index"
	indexed_g "$idx" read-tree --empty || die "could not initialise the bootstrap index"
	# Bootstrap certified whatever it was told: `bootstrap HUB HUB` with an empty HUB and no
	# board returned 0 and announced "2 lane(s) + board" over a tree holding one empty
	# entry. Refuse the duplicate, refuse the empty source, and announce what was BUILT.
	local lane path blob bsize board=0
	local -A seen_paths=()
	local -a bootstrap_paths=() bootstrap_blobs=()
	for lane in "$@"; do
		path="$(lane_path_for "$lane")"
		[ ! -v "seen_paths[$path]" ] ||
			die "lane '$lane' is named twice — a bootstrap manifest has no duplicates"
		seen_paths[$path]=1
		[ -f "$ROOT/$path" ] || die "bootstrap source '$path' does not exist in the checkout"
		blob="$(g hash-object -w -- "$ROOT/$path")" || die "hash-object failed for $path"
		bsize="$(g cat-file -s "$blob")" || die "could not size the bootstrap blob for '$path'"
		[ "$bsize" -gt 0 ] ||
			die "bootstrap source '$path' is empty — an empty status is a mistake, not a message"
		indexed_g "$idx" update-index --add --cacheinfo "100644,$blob,$path" ||
			die "update-index failed for $path"
		bootstrap_paths+=("$path")
		bootstrap_blobs+=("$blob")
	done
	if [ -f "$ROOT/$BOARD_FILE" ]; then
		blob="$(g hash-object -w -- "$ROOT/$BOARD_FILE")" || die "hash-object failed for the board"
		bsize="$(g cat-file -s "$blob")" || die "could not size the bootstrap board blob"
		[ "$bsize" -gt 0 ] || die "board file '$BOARD_FILE' is empty — refused"
		indexed_g "$idx" update-index --add --cacheinfo "100644,$blob,$BOARD_FILE" ||
			die "update-index failed for the board"
		bootstrap_paths+=("$BOARD_FILE")
		bootstrap_blobs+=("$blob")
		board=1
	fi
	local tree newc
	tree="$(indexed_g "$idx" write-tree)" || die "write-tree failed"
	newc="$(printf 'docs(status): bootstrap the live status stream\n\nStatus-Board: hub\nSigned-off-by: %s <%s>\n' \
		"$SANCTIONED_NAME" "$SANCTIONED_EMAIL" | g commit-tree "$tree")" ||
		die "commit-tree failed"
	verify_built_identity "$newc" # bootstrap has a separate commit construction site
	validate_history "$newc" ""
	# Push from a PRIVATE PROJECTION, never from the mutable checkout. Bootstrap used to
	# hash from $ROOT and push from $ROOT, so anything that changed a source file between
	# commit-tree and the push left the hook judging bytes the commit did not carry — a
	# fixture mutated a lane after commit-tree and the hook observed MUTATED-BEFORE-HOOK
	# while the commit and the remote carried HASHED-BEFORE-HOOK, with rc=0. Materialising
	# each published blob by OID and verifying the staged entry makes what the hook sees
	# equal to what is published, by construction. Same guarantee do_publish already had.
	open_main_projection
	local bi=0
	while [ "$bi" -lt "${#bootstrap_paths[@]}" ]; do
		stage_one_in_projection "${bootstrap_paths[$bi]}" "${bootstrap_blobs[$bi]}"
		bi=$((bi + 1))
	done
	if ! clean_git "$PROJ_WT" push -q "$STATUS_REMOTE_URL" "$newc:$LIVE_REF" \
		2>"$WORK/bootstrap-push.err"; then
		# Same reconciliation the publish path performs: the remote may have taken this
		# exact commit and lost the answer, and a second operator may have bootstrapped
		# first. Announcing a flat failure over either is a false statement about a ref
		# that exists.
		local now_bs
		now_bs="$(remote_tip)" || exit $?
		if [ "$now_bs" = "$newc" ]; then
			echo "status-ref: the bootstrap push reported failure, but $LIVE_REF IS $newc —" >&2
			echo "status-ref: the remote accepted it and the answer was lost in transport:" >&2
			echo "status-ref: $(head -1 "$WORK/bootstrap-push.err" 2>/dev/null)" >&2
			g update-ref "$TRACKING_REF" "$newc" ||
				die "could not record the bootstrap tip after a transport failure"
			echo "status-ref: bootstrapped $LIVE_REF at $newc — confirmed after a transport failure"
			return 0
		fi
		[ -z "$now_bs" ] ||
			die "bootstrap lost the race: $LIVE_REF already exists at $now_bs — it runs exactly once"
		die "bootstrap push failed: $(head -1 "$WORK/bootstrap-push.err" 2>/dev/null)"
	fi
	local final
	final="$(remote_tip)" || exit $?
	[ "$final" = "$newc" ] || die "bootstrap verification failed: remote is $final"
	g update-ref "$TRACKING_REF" "$newc" || die "could not record validated bootstrap tip"
	# Count what the TREE holds, not what the argv promised.
	local listing="$WORK/bootstrap-listing" built=0 entry
	g ls-tree -r -z --name-only "$newc" >"$listing" || die "could not enumerate the bootstrap tree"
	while IFS= read -r -d '' entry; do
		[ "$entry" = "$BOARD_FILE" ] || built=$((built + 1))
	done <"$listing"
	rm -f "$listing"
	local board_note="without the board"
	[ "$board" -eq 0 ] || board_note="plus the board"
	echo "status-ref: bootstrapped $LIVE_REF at $newc with $built lane(s), $board_note"
}

cmd="${1:-}"
shift || true
case "$cmd" in
publish|publish-board|read|project|bootstrap)
	STATUS_REMOTE_URL="$(select_status_remote_url)" || exit $?
	;;
*)
	cat >&2 <<'EOF'
usage: status-ref.sh publish <LANE> <draft-file>     # lane status update (fast-forward, no bypass)
       status-ref.sh publish-board <draft-file>      # hub board update (hub only by protocol)
       status-ref.sh read <LANE> | read --all        # fetch + validate + print live status
       status-ref.sh project --all <main-worktree>   # integrator: stage live tree into main
       status-ref.sh bootstrap <lane...>             # hub, exactly once: create refs/status/live
EOF
	exit 64
	;;
esac
case "$cmd" in
publish) do_publish "$@" ;;
publish-board) do_publish --board "$@" ;;
read) do_read "$@" ;;
project) do_project "$@" ;;
bootstrap) do_bootstrap "$@" ;;
esac
