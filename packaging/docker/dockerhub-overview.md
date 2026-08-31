# Olivares AI

**Ground truth for enterprise AI.** Integrate, manage and secure AI in your enterprise —
one ground truth: Claude Code at the deepest level, Codex and Grok Build alongside. A self-hosted engine that gives your AI the context,
resource access and managed sessions to work in a real organization, and gives you the
granular permissions, policies, budgets and tamper-evident audit evidence to run it across
your infrastructure — shipped as **one static binary** with the web console embedded.

- **Source & docs:** https://github.com/olivaresai/olivares
- **Documentation:** https://olivares.ai/docs
- **License:** AGPL-3.0-only (complete product) + Apache-2.0 (connector SDK). A commercial
  exception is available — see `LICENSING.md` in the repository.

> **Status:** beta. APIs and the module surface may still change before v1.

---

## Supported tags

Images are multi-arch (`linux/amd64`, `linux/arm64`) unless noted. Pull by an immutable
version tag or a digest in production; never rely on `latest` for a reproducible deploy.

| Tag | Contents | Architectures |
|-----|----------|---------------|
| `26.8.0`, `latest` | Base engine + embedded web console | amd64, arm64 |
| `26.8.0-fips` | FIPS 140-3 build (`GOFIPS140`, CMVP-validated module) | amd64 |
| `26.8.0-stig` | STIG-hardened (UBI-micro, OpenSCAP-profiled) base image | amd64 |

**This is the official registry for Olivares AI.** Releases are built and signed on GitHub
Container Registry and published here **by digest** with `cosign copy`, so the layers, cosign
signatures and in-toto attestations (SBOM, OpenVEX, SLSA provenance) are identical. `ghcr.io/olivaresai/olivares` is kept as the **fallback** coordinate — the same
digest, and no rate limit on anonymous pulls, which is what to reach for if Docker Hub's
anonymous-pull limit affects a CI node or a large fleet (or just `docker login` first).

## Run it — secure by default

Production listeners are TLS-on-by-default and bind loopback; first boot prints a one-time
setup token and there are no default credentials. Mount a persistent data volume:

```sh
docker run -d --name olivares -p 127.0.0.1:8443:8443 -p 127.0.0.1:8444:8444 \
  -v olivares-data:/var/lib/olivares \
  --user 65532:65532 --read-only --tmpfs /tmp --cap-drop ALL \
  --security-opt no-new-privileges \
  olivaresai/olivares:26.8.0 \
  serve --listen 0.0.0.0:8443 --grpc-listen 0.0.0.0:8444 --data-dir /var/lib/olivares
```

(`--listen 0.0.0.0` is required inside the container — the engine binds loopback by default;
the `-p 127.0.0.1:…` host mapping keeps it loopback-only on the host.)

Open `https://127.0.0.1:8443` and read the one-time first-boot setup token from the logs
(`docker logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'`). The data directory holds the SQLite
store, the append-only audit signing key and the TLS material — back it up and protect it.

## Explore the demo (evaluation)

A synthetic estate on loopback, plaintext — the console, API and audit ledger are the real
product; only the data is seeded. Never for real data:

```sh
docker run --rm -p 127.0.0.1:8443:8443 --tmpfs /data:uid=65532,gid=65532 \
  olivaresai/olivares:latest \
  serve --seed-demo --insecure --listen 0.0.0.0:8443 --data-dir /data
```

Open `http://127.0.0.1:8443` and log in with the demo credentials printed in the boot banner.

For Docker Compose (single-node SQLite, multi-tenant Postgres, and a DR backup profile)
and Kubernetes/Helm, see the deployment guides in the repository under `deploy/` and the
documentation site.

## Verify the image

Every image is signed keylessly (Sigstore/cosign) and carries SBOM and SLSA Build L3
provenance attestations — all preserved on this registry by digest:

```sh
cosign verify olivaresai/olivares:26.8.0 \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Full verification (SBOM, OpenVEX, SLSA) is documented at
https://github.com/olivaresai/olivares/blob/main/docs/RELEASE-VERIFICATION.md

## Security

Report vulnerabilities privately to `security@olivares.ai` — never a public issue.
See `SECURITY.md` in the repository.
