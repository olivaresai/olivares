#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Fedora 44 DNF5 client for one rpm-md repository: a local directory (the
# transaction container has --network none), or with --baseurl the reviewed
# HTTPS origin, staged or promoted (the container then has network).
# The image is pinned by digest. pkg_gpgcheck=1 and repo_gpgcheck=1 come from
# dnf-repo.template, and the trust anchor is always the pinned --gpgkey file,
# never a key fetched from the repository.
#
# The pinned image has no python3. Inside it the client runs only bash, the
# coreutils and findutils it ships, grep, rpm, rpmkeys and dnf5; it writes
# `dnf5 repo info --json` to --out-dir, and the effective gpg checks are parsed
# on the runner host afterwards (check_repo_info).
set -euo pipefail
LC_ALL=C
export LC_ALL

# registry.fedoraproject.org/v2/fedora/manifests/44
# 2026-09-27T10:43:27Z Docker-Content-Digest (OCI index).
FEDORA_44_IMAGE="registry.fedoraproject.org/fedora@sha256:539cadb5d8a43564d8abefd6eafdfcbcd4809070efbb900ec248229903db5911"
# The published repository, canonical or one staging prefix; $basearch is
# expanded by DNF5 inside the client.
REVIEWED_BASEURL='^https://packages[.]olivares[.]ai(/staging/run-[0-9]+-attempt-[0-9]+)?/(stable|security)/rpm/[$]basearch$'

client_root() {
	cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd
}

repo_template_path() {
	if [[ -n "${OLIVARES_DNF_REPO_TEMPLATE:-}" ]]; then
		printf '%s\n' "$OLIVARES_DNF_REPO_TEMPLATE"
		return 0
	fi
	printf '%s\n' "$(client_root)/../packaging/repositories/dnf-repo.template"
}

repo_file_is_closed() {
	local file="$1"
	[[ -f "$file" ]] || return 1
	grep -qx 'pkg_gpgcheck=1' "$file" || return 1
	grep -qx 'repo_gpgcheck=1' "$file" || return 1
	if grep -E '^[[:space:]]*gpgcheck=' "$file" >/dev/null; then
		return 1
	fi
	return 0
}

render_repo_file() {
	local repo_id="$1" repo_name="$2" baseurl="$3" key_url="$4" destination="$5"
	local template text rendered forced
	template=$(repo_template_path)
	[[ -f "$template" ]] || {
		printf 'dnf-repository-client: could not check — repo template is absent\n' >&2
		return 2
	}
	if [[ "$baseurl" != file:///* && ! "$baseurl" =~ $REVIEWED_BASEURL ]]; then
		printf 'dnf-repository-client: refused — baseurl must be file:// or the reviewed HTTPS origin\n' >&2
		return 1
	fi
	case "$key_url" in
	file:///*) ;;
	*) printf 'dnf-repository-client: refused — gpgkey must be a file:// path\n' >&2; return 1 ;;
	esac
	case "$repo_id" in
	[A-Za-z0-9._-]*) ;;
	*) printf 'dnf-repository-client: refused — repo id is unsafe\n' >&2; return 1 ;;
	esac
	text=$(cat -- "$template")
	rendered=${text//\{\{REPO_ID\}\}/$repo_id}
	rendered=${rendered//\{\{REPO_NAME\}\}/$repo_name}
	rendered=${rendered//\{\{BASEURL\}\}/$baseurl}
	rendered=${rendered//\{\{GPGKEY\}\}/$key_url}
	if [[ "$rendered" == *"{{"* ]]; then
		printf 'dnf-repository-client: refused — repo template left a placeholder\n' >&2
		return 1
	fi
	if [[ -n "${OLIVARES_RPM_SELF_TEST_FORCE_REPO_GPGCHECK:-}" ]]; then
		forced="${OLIVARES_RPM_SELF_TEST_FORCE_REPO_GPGCHECK}"
		rendered=${rendered/repo_gpgcheck=1/repo_gpgcheck=${forced}}
	fi
	printf '%s\n' "$rendered" >"$destination"
	repo_file_is_closed "$destination"
}

# check_repo_info FILE: DNF5's effective settings for the olivares repository
# must have pkg_gpgcheck and repo_gpgcheck true. Runs where python3 exists (the
# runner host, or the tools image that pins it); exit 1 if either is not true or
# FILE is a symlink or not a regular file (lstat; the read never follows a link),
# exit 2 if the file is missing, empty or unreadable.
check_repo_info() {
	local info="$1"
	if [[ -L "$info" ]] || { [[ -e "$info" ]] && [[ ! -f "$info" ]]; }; then
		printf 'dnf-repository-client: refused — repo info is not a regular file: %s\n' "$info" >&2
		return 1
	fi
	[[ -f "$info" && -s "$info" ]] || {
		printf 'dnf-repository-client: could not check — dnf5 repo info was not written\n' >&2
		return 2
	}
	command -v python3 >/dev/null 2>&1 || {
		printf 'dnf-repository-client: could not check — python3 is absent where repo info is parsed\n' >&2
		return 2
	}
	python3 - "$info" <<'PY'
import json
import os
import stat
import sys

try:
    handle = os.open(sys.argv[1], os.O_RDONLY | os.O_NOFOLLOW)
except OSError:
    print("dnf-repository-client: refused — repo info is not a regular file", file=sys.stderr)
    raise SystemExit(1)
with os.fdopen(handle, "rb") as stream:
    if not stat.S_ISREG(os.fstat(stream.fileno()).st_mode):
        print("dnf-repository-client: refused — repo info is not a regular file", file=sys.stderr)
        raise SystemExit(1)
    raw = stream.read()
try:
    payload = json.loads(raw.decode("utf-8"))
except (UnicodeError, json.JSONDecodeError):
    print("dnf-repository-client: could not check — dnf5 repo info is not JSON", file=sys.stderr)
    raise SystemExit(2)
rows = payload if isinstance(payload, list) else [payload]
match = [row for row in rows if isinstance(row, dict) and row.get("id") == "olivares"]
if len(match) != 1:
    print("dnf-repository-client: refused — dnf5 repo info does not describe exactly one olivares", file=sys.stderr)
    raise SystemExit(1)
for key in ("pkg_gpgcheck", "repo_gpgcheck"):
    if match[0].get(key) is not True:
        print(f"dnf-repository-client: refused — effective {key} is not true", file=sys.stderr)
        raise SystemExit(1)
print("dnf-repository-client: effective pkg_gpgcheck=true repo_gpgcheck=true")
PY
}

run_host() {
	local repo="$1" gpgkey="$2" expect="$3" image="$4" package_name="$5" baseurl="$6" version="$7"
	local evidence_file="$8" staging_id="$9"
	if [[ -n "$baseurl" ]]; then
		[[ -z "$repo" ]] || {
			printf 'dnf-repository-client: could not check — choose one of --repo or --baseurl\n' >&2
			exit 2
		}
		[[ "$baseurl" =~ $REVIEWED_BASEURL ]] || {
			printf 'dnf-repository-client: could not check — --baseurl is not the reviewed HTTPS origin\n' >&2
			exit 2
		}
	else
		[[ "$repo" == /* && -d "$repo" ]] || {
			printf 'dnf-repository-client: could not check — --repo must be an absolute directory\n' >&2
			exit 2
		}
	fi
	if [[ -n "$evidence_file" || -n "$staging_id" ]]; then
		[[ "$expect" == install && "$evidence_file" == /* && ! -e "$evidence_file" ]] || {
			printf 'dnf-repository-client: could not check — evidence needs --expect install and a new absolute --evidence-file\n' >&2
			exit 2
		}
		[[ "$staging_id" =~ ^run-[0-9]+-attempt-[0-9]+$ && "$baseurl" == "https://packages.olivares.ai/staging/$staging_id/"* ]] || {
			printf 'dnf-repository-client: could not check — evidence staging-id differs from --baseurl\n' >&2
			exit 2
		}
	fi
	[[ "$gpgkey" == /* && -f "$gpgkey" ]] || {
		printf 'dnf-repository-client: could not check — --gpgkey must be an absolute file\n' >&2
		exit 2
	}
	case "$image" in
	*@sha256:[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]*) ;;
	*) printf 'dnf-repository-client: refused — image must be pinned by digest\n' >&2; exit 1 ;;
	esac
	case "$image" in
	*latest*) printf 'dnf-repository-client: refused — an unpinned latest tag is not the client image\n' >&2; exit 1 ;;
	esac
	command -v docker >/dev/null 2>&1 || {
		printf 'dnf-repository-client: could not check — docker is absent\n' >&2
		exit 2
	}
	local scripts templates
	scripts=$(client_root)
	templates=$(cd -- "$(client_root)/../packaging/repositories" && pwd)
	local docker_args=(run --rm --security-opt no-new-privileges
		--env CLIENT_PACKAGE="$package_name" --env CLIENT_VERSION="$version"
		--env OLIVARES_DNF_REPO_TEMPLATE=/opt/olivares-template/dnf-repo.template
		--volume "$gpgkey:/pinned/repository-key.asc:ro"
		--volume "${scripts}:/opt/olivares-client:ro"
		--volume "${templates}:/opt/olivares-template:ro")
	local inside_repo=(--repo /repository)
	if [[ -n "$baseurl" ]]; then
		# The HTTPS client needs the network.
		docker_args+=(--env CLIENT_BASEURL="$baseurl")
		inside_repo=()
	else
		# The local client transaction container has no network.
		docker_args+=(--network none --volume "$repo:/repository:ro")
	fi
	local out rc=0
	out=$(mktemp -d "${TMPDIR:-/tmp}/dnf-client.XXXXXX") || {
		printf 'dnf-repository-client: could not check — no scratch directory for repo info\n' >&2
		exit 2
	}
	# The container runs as root, and in HTTPS mode with network, so its one
	# writable mount is the single output file, created empty here: it cannot
	# add files or replace this one with a link. Every other mount is read-only.
	: >"$out/repo-info.json"
	docker_args+=(--volume "$out/repo-info.json:/client-out/repo-info.json")
	docker "${docker_args[@]}" "$image" \
		/bin/bash /opt/olivares-client/dnf-repository-client.sh --inside \
		"${inside_repo[@]}" --gpgkey /pinned/repository-key.asc --expect "$expect" --out-dir /client-out || rc=$?
	if [[ "$rc" -eq 0 && "$expect" == install ]]; then
		check_repo_info "$out/repo-info.json" || rc=$?
	fi
	rm -rf -- "$out"
	[[ "$rc" -eq 0 ]] || exit "$rc"
	if [[ -n "$evidence_file" ]]; then
		printf '%s\n' "$staging_id" >"$evidence_file"
		chmod 0600 "$evidence_file"
	fi
}

run_inside() {
	local expect="$1" package_name="$2" repo="$3" gpgkey="$4" out_dir="$5"
	local baseurl="${CLIENT_BASEURL:-}" version="${CLIENT_VERSION:-}"
	if [[ -n "$baseurl" ]]; then
		[[ -z "$repo" && "$baseurl" =~ $REVIEWED_BASEURL ]] || {
			printf 'dnf-repository-client: could not check — the inside baseurl is not the reviewed origin\n' >&2
			exit 2
		}
	else
		[[ "$repo" == /* && -d "$repo" ]] || {
			printf 'dnf-repository-client: could not check — repository mount is absent\n' >&2
			exit 2
		}
		baseurl="file://${repo}"
	fi
	[[ "$gpgkey" == /* && -f "$gpgkey" && ! -L "$gpgkey" ]] || {
		printf 'dnf-repository-client: could not check — pinned key mount is absent\n' >&2
		exit 2
	}
	command -v dnf5 >/dev/null 2>&1 || {
		printf 'dnf-repository-client: could not check — dnf5 is absent\n' >&2
		exit 2
	}
	command -v rpm >/dev/null 2>&1 || {
		printf 'dnf-repository-client: could not check — rpm is absent\n' >&2
		exit 2
	}
	command -v rpmkeys >/dev/null 2>&1 || {
		printf 'dnf-repository-client: could not check — rpmkeys is absent\n' >&2
		exit 2
	}
	if [[ -z "${OLIVARES_DNF_REPO_TEMPLATE:-}" && -f /opt/olivares-template/dnf-repo.template ]]; then
		export OLIVARES_DNF_REPO_TEMPLATE=/opt/olivares-template/dnf-repo.template
	fi
	local drop
	for drop in /etc/yum.repos.d /etc/distro.repos.d /usr/share/dnf5/repos.d; do
		[[ -d "$drop" ]] || continue
		find "$drop" -maxdepth 1 -type f -name '*.repo' -delete
	done
	mkdir -p /etc/dnf /etc/yum.repos.d
	printf '%s\n' '[main]' 'reposdir=/etc/yum.repos.d' 'gpgcheck_policy=legacy' >/etc/dnf/dnf.conf
	local repo_path rc outcome
	repo_path=/etc/yum.repos.d/olivares.repo
	render_repo_file olivares "Olivares AI" "$baseurl" "file://${gpgkey}" "$repo_path"
	printf 'dnf-repository-client: wrote repo file with pkg_gpgcheck=1 and repo_gpgcheck=1\n'
	rpmkeys --import "$gpgkey"
	if [[ -z "$package_name" ]]; then
		package_name="${CLIENT_PACKAGE:-}"
	fi
	if [[ -z "$package_name" ]]; then
		[[ -n "$repo" ]] || {
			printf 'dnf-repository-client: could not check — an HTTPS client needs --package\n' >&2
			exit 2
		}
		local names=()
		mapfile -t names < <(find "$repo" -maxdepth 1 -type f -name '*.rpm' -print | sort)
		[[ "${#names[@]}" -ge 1 ]] || {
			printf 'dnf-repository-client: refused — repository has no rpm\n' >&2
			exit 1
		}
		package_name=$(rpm -qp --qf '%{NAME}' "${names[0]}")
	fi
	case "$package_name" in
	[A-Za-z0-9._+-]*) ;;
	*) printf 'dnf-repository-client: refused — package name is unsafe\n' >&2; exit 1 ;;
	esac
	local install_spec="$package_name"
	if [[ -n "$version" ]]; then
		[[ "$version" =~ ^[0-9]+\.[0-9]+(\.[0-9]+)?$ ]] || {
			printf 'dnf-repository-client: could not check — version must be YY.M or YY.M.N\n' >&2
			exit 2
		}
		install_spec="${package_name}-${version}"
	fi
	rpm -q --qf 'dnf-repository-client: client %{NEVRA}\n' dnf5 rpm || true
	set +e
	dnf5 --refresh install -y --disablerepo='*' --enablerepo=olivares "$install_spec"
	rc=$?
	set -e
	if [[ "$rc" -eq 0 ]]; then
		outcome=installed
		[[ "$out_dir" == /* && -d "$out_dir" ]] || {
			printf 'dnf-repository-client: could not check — --out-dir for repo info is absent\n' >&2
			exit 2
		}
		dnf5 repo info --json olivares >"$out_dir/repo-info.json"
		rpm -q "$package_name" >/dev/null
		if [[ -n "$version" && "$(rpm -q --qf '%{VERSION}' "$package_name")" != "$version" ]]; then
			printf 'dnf-repository-client: refused — installed %s is not version %s\n' "$package_name" "$version" >&2
			exit 1
		fi
	else
		outcome=refused
	fi
	printf 'dnf-repository-client: dnf_exit=%s outcome=%s\n' "$rc" "$outcome"
	if [[ "$expect" == "install" && "$outcome" != "installed" ]]; then
		exit 1
	fi
	if [[ "$expect" == "refuse" && "$outcome" != "refused" ]]; then
		exit 1
	fi
}

main() {
	local inside=0 expect="install" repo="" gpgkey="" image="$FEDORA_44_IMAGE" package_name=""
	local baseurl="" version="" evidence_file="" staging_id="" out_dir=""
	while [[ "$#" -gt 0 ]]; do
		case "$1" in
		--repo) repo="${2:-}"; shift 2 ;;
		--gpgkey) gpgkey="${2:-}"; shift 2 ;;
		--expect) expect="${2:-}"; shift 2 ;;
		--image) image="${2:-}"; shift 2 ;;
		--package) package_name="${2:-}"; shift 2 ;;
		--baseurl) baseurl="${2:-}"; shift 2 ;;
		--version) version="${2:-}"; shift 2 ;;
		--evidence-file) evidence_file="${2:-}"; shift 2 ;;
		--staging-id) staging_id="${2:-}"; shift 2 ;;
		--inside) inside=1; shift ;;
		--out-dir) out_dir="${2:-}"; shift 2 ;;
		--check-repo-info) check_repo_info "${2:-}"; exit $? ;;
		-h | --help)
			printf '%s\n' "usage: dnf-repository-client.sh (--repo ABS | --baseurl REVIEWED_HTTPS/\$basearch) --gpgkey ABS" \
				"       [--expect install|refuse] [--image DIGEST] [--package NAME] [--version X.Y.Z]" \
				"       [--evidence-file ABS --staging-id run-N-attempt-N] [--inside]"
			exit 0
			;;
		*) printf 'dnf-repository-client: unknown argument: %s\n' "$1" >&2; exit 2 ;;
		esac
	done
	case "$expect" in
	install | refuse) ;;
	*) printf 'dnf-repository-client: could not check — --expect must be install or refuse\n' >&2; exit 2 ;;
	esac
	if [[ "$inside" -eq 1 ]]; then
		run_inside "$expect" "$package_name" "$repo" "$gpgkey" "$out_dir"
	else
		run_host "$repo" "$gpgkey" "$expect" "$image" "$package_name" "$baseurl" "$version" "$evidence_file" "$staging_id"
	fi
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
	main "$@"
fi
