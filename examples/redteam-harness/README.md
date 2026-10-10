# Isolated redteam HTTP target harness

This operator-provisioned guest implements the existing sandbox runtime job/result
contract for a target that accepts `POST` JSON `{id, surface, payload}` and returns
a plain-text response. It is a protocol adapter; it does not simulate a target or
decide whether its defense passed. The redteam module judges the actual response.

Build the guest statically and put it in a dedicated read-only root filesystem:

```sh
CGO_ENABLED=0 go build -trimpath -o sandbox-harness examples/redteam-harness/main.go
install -d /var/lib/olivares/redteam-rootfs/sandbox /var/lib/olivares/redteam-rootfs/tmp
install -m 755 sandbox-harness /var/lib/olivares/redteam-rootfs/sandbox-harness
```

With gVisor `runsc` installed on Linux, provision the existing
`OLIVARES_SANDBOX_RUNTIME_CONFIG` file with:

```json
{
  "default": "gvisor",
  "gvisor": {
    "binary": "/usr/local/bin/runsc",
    "rootfs_dir": "/var/lib/olivares/redteam-rootfs",
    "bundle_root": "/var/lib/olivares/redteam-bundles",
    "state_root": "/var/lib/olivares/redteam-state",
    "harness_path": "/sandbox-harness",
    "proxy_socket": true,
    "timeout_seconds": 20
  }
}
```

The opt-in `proxy_socket` field exposes only a per-run Unix socket to the guest.
Bytes travel to the engine's existing deny-by-default, target-scoped HTTP proxy;
both HTTP and HTTPS use that gate. The guest keeps its own network namespace,
`--network=none`, read-only root, non-root UID, dropped capabilities and seccomp
profile (explicit `--oci-seccomp`). Use a rootfs with no unrelated host sockets. `runsc` requires the host
privileges described in its [production guide](https://gvisor.dev/docs/user_guide/production/).
Keep the bundle path short enough for Unix socket paths (Linux limit: 108 bytes,
including the terminating NUL); bundle IDs add the target reference and run counter.

This guest supports deterministic scenario mocks too. For probes it requires an
explicit proxy, bounds time and response size, and refuses redirects or non-200
responses. HTTPS targets need their trusted CA certificates in the guest rootfs.
It never bypasses certificate validation or falls back to a direct connection.
An unconfigured runtime keeps the existing offline/skipped behavior. Existing
TCP-only harnesses keep working with `proxy_socket` omitted or false.

Turn on `redteam` using the existing module selection, create your agent through
the inventory API, register its reachable endpoint, then explicitly authorize it
before launching a suite. Registration alone grants no consent. Only authorized
administrators may authorize or launch; execution faults and unverified instance
destruction contribute no defense score. A deterministic local target qualifies
transport and scoring behavior, not a vendor model's robustness.
