# Release distribution index

`release-index.json` is the deterministic inventory that distribution adapters consume. It is
derived from the release's authenticated `checksums.txt`, the bytes named there and the exact
`release-commit.txt`; it is not a second hand-maintained list of artifacts.

The versioned contract is [`contracts/release-index.schema.json`](contracts/release-index.schema.json).
The index records:

- the release version, tag, commit, channel and source repository;
- every checksummed file with its digest, byte size, kind and immutable release URL;
- each immutable OCI reference supplied by the release producer; and
- an explicit state for GitHub Releases, OTA stable, Homebrew, GHCR, Docker Hub and Helm.

`scripts/render-release-index.sh` refuses malformed or duplicate checksum rows, unsafe names,
missing bytes, digest mismatches, a commit not bound by `checksums.txt`, duplicate images and a
missing or duplicate distribution surface. Its output contains no clock, host path or iteration
order, so the same inputs produce the same bytes.

`scripts/check-release-index.sh` does not trust the index's own claims. The caller supplies the
expected release identity, images and surface states; the checker regenerates the complete index
from the release inputs and compares it byte for byte. A surface state is therefore evidence that
the caller must have measured, not a value copied back out of the document being checked.

This first contract does not promote a channel and does not claim that a staged surface is live.
The release workflow will sign and attach the generated index when the promotion row is wired;
the later parity gate is responsible for proving `published` against each remote surface.
