#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

# Run only on a disposable Docker host. Credentials are generated for these
# containers and passed through private files, never command arguments.
# shellcheck disable=SC2016 # Backticks in printf formats are literal Markdown.
set -euo pipefail
set +x
umask 077
root="$(cd "$(dirname "$0")/.." && pwd)"
report="${1:?usage: dr-offsite-targets.sh REPORT.md}"
source_sha="${2:-$(git -C "$root" rev-parse HEAD)}"
scratch="$(mktemp -d "${TMPDIR:-$root}/dr-offsite.XXXXXXXX")"
export TMPDIR="$scratch/tmp"
mkdir "$TMPDIR"
containers=()
new_images=()
# shellcheck disable=SC2317 # Invoked by the EXIT trap.
cleanup() {
  local rc=$?
  trap - EXIT
  for container in "${containers[@]}"; do
    if docker container inspect "$container" >/dev/null 2>&1; then
      docker rm -fv "$container" >/dev/null || rc=1
    fi
  done
  for image in "${new_images[@]}"; do
    docker image rm "$image" >/dev/null || rc=1
  done
  chmod -R u+w "$scratch"
  rm -rf "$scratch"
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
cd "$root"
printf '# Tested offsite targets\n\nSource: `%s`\n\n' "$source_sha" > "$report"
printf '| Target | Image | Last pass (UTC) | Result |\n|---|---|---|---|\n' >> "$report"
ensure_image() {
  if ! docker image inspect "$1" >/dev/null 2>&1; then
    new_images+=("$1")
    docker pull "$1"
  fi
}

# The host toolchain is set up by CI. The lab VM can use the same pinned Go
# toolchain in a disposable compiler container; CGO is unnecessary for SQLite.
run_go() {
  if command -v go >/dev/null; then
    CGO_ENABLED=0 GOMAXPROCS=2 "$@"
  else
    local image=golang:1.26.10@sha256:36775b15988ba37410576be98e8628fc1bf25a09c3d9afc7f5e3c3d328a063ae
    ensure_image "$image"
    docker run --rm --name "${name}-compiler" --cpus 2 --memory 4g \
      --user "$(id -u):$(id -g)" \
      -v "$root:$root:ro" -v "$scratch:$scratch" -w "$root" \
      -e CGO_ENABLED=0 -e GOMAXPROCS=2 -e GOWORK -e GOENV=off -e TMPDIR \
      -e "GOPATH=$scratch/gopath" \
      -e "GOCACHE=$scratch/gocache" -e "GOMODCACHE=$scratch/gomodcache" \
      "$image" "$@"
  fi
}
name="${scratch##*/}"
name="${name,,}"
containers+=("${name}-compiler")
GOWORK=off run_go go -C "$root/core" test -p 2 -mod=readonly -c -o "$scratch/core.test" ./dr
GOWORK=off run_go go -C "$root/core" test -p 2 -mod=readonly -c -o "$scratch/api.test" ./api
GOWORK=off run_go go -C "$root/cmd/olivares" test -p 2 -mod=readonly -c -o "$scratch/cli.test" .

# Build archived MinIO source without modifications, independently of historical
# registry tags. The archive digest fixes the exact release input.
curl --fail --silent --show-error --location --max-time 120 \
  https://codeload.github.com/minio/minio/tar.gz/RELEASE.2025-10-15T17-29-55Z -o "$scratch/minio.tar.gz"
printf '%s  %s\n' be6d0bd3696c3a13a35f02d3a0280b64319c67918b4501c5c3d87f96d000085c "$scratch/minio.tar.gz" | sha256sum -c -
mkdir "$scratch/minio"
tar -xzf "$scratch/minio.tar.gz" -C "$scratch/minio" --strip-components=1
GOWORK=off run_go go -C "$scratch/minio" build -p 2 -trimpath \
  -ldflags '-s -w -X github.com/minio/minio/cmd.Version=2025-10-15T17:29:55Z -X github.com/minio/minio/cmd.ReleaseTag=RELEASE.2025-10-15T17-29-55Z -X github.com/minio/minio/cmd.CommitID=9e49d5e7a648f00e26f2246f4dc28e6b07f8c84a' \
  -o "$scratch/minio-server" .
minio_image="${name}-minio:2025-10-15"
new_images+=("$minio_image")
# Use an empty context except for the binary, never credential files.
mkdir "$scratch/image"
mv "$scratch/minio-server" "$scratch/image/minio"
printf 'FROM scratch\nCOPY minio /minio\nENTRYPOINT ["/minio"]\n' > "$scratch/image/Dockerfile"
docker build --no-cache -t "$minio_image" "$scratch/image"

failed=0
for target in minio seaweedfs; do
  access="$(od -An -N10 -tx1 /dev/urandom | tr -d ' \n')"
  secret="$(od -An -N20 -tx1 /dev/urandom | tr -d ' \n')"
  printf '%s\n' "$access" > "$scratch/access"
  printf '%s\n' "$secret" > "$scratch/secret"
  printf '%s\n%s\n' "$access" "$secret" > "$scratch/credential-patterns"
  container="${name}-${target}"
  containers+=("$container")
  case "$target" in
    minio)
      image="$minio_image"
      release=RELEASE.2025-10-15T17-29-55Z
      printf 'MINIO_ROOT_USER=%s\nMINIO_ROOT_PASSWORD=%s\n' "$access" "$secret" > "$scratch/minio.env"
      docker run -d --name "$container" --cpus 2 --memory 1g --pids-limit 256 \
        --security-opt no-new-privileges --cap-drop ALL --env-file "$scratch/minio.env" \
        -p 127.0.0.1::9000 "$image" server /data > /dev/null
      endpoint="http://$(docker port "$container" 9000/tcp)"
      ;;
    seaweedfs)
      image=chrislusf/seaweedfs:4.51@sha256:f7a49e3f3972ebed5be2ed4befa4a0c68eb7bdddba6e692d42729ceb545134b8
      release=4.51
      ensure_image "$image"
      printf '{"identities":[{"name":"qualification","credentials":[{"accessKey":"%s","secretKey":"%s"}],"actions":["Admin","Read","Write","List"]}]}\n' \
        "$access" "$secret" > "$scratch/s3.json"
      docker run -d --name "$container" --cpus 2 --memory 1g --pids-limit 256 \
        --security-opt no-new-privileges --cap-drop ALL \
        --user "$(id -u):$(id -g)" \
        --tmpfs "/data:rw,uid=$(id -u),gid=$(id -g),mode=0700,size=512m" \
        -v "$scratch/s3.json:/etc/seaweedfs/s3.json:ro" -p 127.0.0.1::8333 \
        "$image" server -s3 -s3.config=/etc/seaweedfs/s3.json -dir=/data \
        -master.volumeSizeLimitMB=32 -volume.max=4 > /dev/null
      endpoint="http://$(docker port "$container" 8333/tcp)"
      ;;
  esac
  # curl's native SigV4 implementation creates the test bucket. Only its config
  # path appears in argv. No cloud account, cloud endpoint or SDK is involved.
  printf 'url = "%s/dr-qualification"\nuser = "%s:%s"\naws-sigv4 = "aws:amz:us-east-1:s3"\nrequest = "PUT"\ndata = ""\n' \
    "$endpoint" "$access" "$secret" > "$scratch/curl.conf"
  ready=false
  for _ in $(seq 1 60); do
    if curl --silent --fail --max-time 2 --config "$scratch/curl.conf" > "$scratch/bucket.log" 2>&1; then
      ready=true
      break
    fi
    sleep 1
  done
  # A bucket PUT can succeed before the filer has its lock ring, and object writes or deletes then
  # stall until the test client times out (run 40861881344). Ready means a real object round trip.
  if "$ready"; then
    printf 'url = "%s/dr-qualification/readiness/probe.bin"\nuser = "%s:%s"\naws-sigv4 = "aws:amz:us-east-1:s3"\nrequest = "PUT"\ndata = "ready"\n' \
      "$endpoint" "$access" "$secret" > "$scratch/curl-probe-put.conf"
    printf 'url = "%s/dr-qualification/readiness/probe.bin"\nuser = "%s:%s"\naws-sigv4 = "aws:amz:us-east-1:s3"\nrequest = "DELETE"\n' \
      "$endpoint" "$access" "$secret" > "$scratch/curl-probe-delete.conf"
    ready=false
    for _ in $(seq 1 60); do
      if curl --silent --fail --max-time 5 --config "$scratch/curl-probe-put.conf" >> "$scratch/bucket.log" 2>&1 &&
         curl --silent --fail --max-time 5 --config "$scratch/curl-probe-delete.conf" >> "$scratch/bucket.log" 2>&1; then
        ready=true
        break
      fi
      sleep 1
    done
  fi
  # MinIO also qualifies the immutable copy: a bucket created with Object Lock
  # and a write-only credential. AssumeRole with a session policy mints that
  # credential (put, read, list, read the lock configuration; no delete), so the
  # pruner's root credential and the writer's are different, as in production.
  cli_tests='^TestDROffsiteLiveBucket$'
  if "$ready" && [[ "$target" == minio ]]; then
    printf 'url = "%s/dr-immutable"\nuser = "%s:%s"\naws-sigv4 = "aws:amz:us-east-1:s3"\nrequest = "PUT"\ndata = ""\nheader = "x-amz-bucket-object-lock-enabled: true"\n' \
      "$endpoint" "$access" "$secret" > "$scratch/curl-lock.conf"
    printf '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:PutObject","s3:PutObjectRetention","s3:GetObject","s3:ListBucket","s3:GetBucketObjectLockConfiguration"],"Resource":["arn:aws:s3:::dr-immutable","arn:aws:s3:::dr-immutable/*"]}]}\n' \
      > "$scratch/writer-policy.json"
    printf 'url = "%s/"\nuser = "%s:%s"\naws-sigv4 = "aws:amz:us-east-1:sts"\ndata = "Action=AssumeRole&Version=2011-06-15&DurationSeconds=3600"\ndata-urlencode = "Policy@%s"\n' \
      "$endpoint" "$access" "$secret" "$scratch/writer-policy.json" > "$scratch/curl-sts.conf"
    if curl --silent --fail --max-time 10 --config "$scratch/curl-lock.conf" >> "$scratch/bucket.log" 2>&1 &&
       curl --silent --fail --max-time 10 --config "$scratch/curl-sts.conf" > "$scratch/sts.xml" 2>> "$scratch/bucket.log"; then
      for field in AccessKeyId SecretAccessKey SessionToken; do
        sed -n "s:.*<$field>\\([^<]*\\)</$field>.*:\\1:p" "$scratch/sts.xml" > "$scratch/writer-$field"
        test -s "$scratch/writer-$field" || ready=false
        cat "$scratch/writer-$field" >> "$scratch/credential-patterns"
      done
      rm -f "$scratch/sts.xml"
      export OLIVARES_TEST_OFFSITE_LOCK_BUCKET=dr-immutable
      export OLIVARES_TEST_OFFSITE_WRITER_ACCESS_FILE="$scratch/writer-AccessKeyId"
      export OLIVARES_TEST_OFFSITE_WRITER_SECRET_FILE="$scratch/writer-SecretAccessKey"
      export OLIVARES_TEST_OFFSITE_WRITER_TOKEN_FILE="$scratch/writer-SessionToken"
      cli_tests='^(TestDROffsiteLiveBucket|TestDROffsiteImmutableLiveBucket)$'
    else
      printf '%s: Object Lock bucket or write-only credential was not created\n' "$target" >> "$scratch/bucket.log"
      ready=false
    fi
  fi
  status=FAIL
  log="$scratch/$target.log"
  if "$ready"; then
    export OLIVARES_TEST_OFFSITE_ENDPOINT="$endpoint"
    export OLIVARES_TEST_OFFSITE_ACCESS_FILE="$scratch/access"
    export OLIVARES_TEST_OFFSITE_SECRET_FILE="$scratch/secret"
    if (cd core/dr && "$scratch/core.test" -test.v -test.run '^TestOffsiteLiveInterruptedUpload$' -test.timeout 2m) > "$log" 2>&1 &&
       (cd cmd/olivares && "$scratch/cli.test" -test.v -test.run "$cli_tests" -test.timeout 5m) >> "$log" 2>&1 &&
       { [[ "$target" != minio ]] || (cd core/api && "$scratch/api.test" -test.v -test.run '^TestDROffsiteScheduleLiveWriteOnlyCredential$' -test.timeout 2m) >> "$log" 2>&1; } &&
       grep -q -- '--- PASS: TestOffsiteLiveInterruptedUpload ' "$log" &&
       grep -q -- '--- PASS: TestDROffsiteLiveBucket ' "$log" &&
       { [[ "$target" != minio ]] || grep -q -- '--- PASS: TestDROffsiteImmutableLiveBucket ' "$log"; } &&
       { [[ "$target" != minio ]] || grep -q -- '--- PASS: TestDROffsiteScheduleLiveWriteOnlyCredential ' "$log"; } &&
       ! grep -q -- '--- SKIP:' "$log"; then
      status=PASS
    fi
    unset OLIVARES_TEST_OFFSITE_LOCK_BUCKET OLIVARES_TEST_OFFSITE_WRITER_ACCESS_FILE \
      OLIVARES_TEST_OFFSITE_WRITER_SECRET_FILE OLIVARES_TEST_OFFSITE_WRITER_TOKEN_FILE
  else
    printf '%s: bucket creation did not succeed\n' "$target" > "$log"
  fi
  if [[ "$target" == minio ]]; then
    docker exec "$container" /minio --version > "$scratch/version.log" 2>&1
  else
    docker exec "$container" weed version > "$scratch/version.log" 2>&1
  fi
  if ! grep -Fq "$release" "$scratch/version.log"; then
    status=FAIL
  fi
  # Check before emitting any captured output. A match fails without exposing it.
  docker logs "$container" > "$scratch/server.log" 2>&1
  if grep -Fqf "$scratch/credential-patterns" "$log" "$scratch/server.log" "$scratch/bucket.log" "$scratch/version.log"; then
    printf '%s: credential appeared in captured output (output withheld)\n' "$target" >&2
    status=FAIL
  else
    scan_rc=$?
    if [[ "$scan_rc" != 1 ]]; then
      printf '%s: credential scan could not read all output (output withheld)\n' "$target" >&2
      status=FAIL
    else
      cat "$scratch/version.log" "$log"
      if [[ "$status" != PASS ]]; then
        cat "$scratch/server.log" "$scratch/bucket.log"
      fi
      printf '%s: credential log scan passed\n' "$target"
    fi
  fi
  digest="$(docker image inspect --format '{{.Id}}' "$image")"
  if [[ "$status" == PASS ]]; then
    printf '| %s %s | `%s` | %s | PASS |\n' "$target" "$release" "$digest" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >> "$report"
  else
    printf '| %s %s | `%s` | — | FAIL (see job log) |\n' "$target" "$release" "$digest" >> "$report"
    failed=1
  fi
  docker rm -fv "$container" > /dev/null
  containers=("${name}-compiler")
done
cat "$report"
exit "$failed"
