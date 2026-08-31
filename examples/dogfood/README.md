# Dogfood source configuration (redacted template)

This directory is **not a runnable example**, and that is why it has no smoke test: it is the
shape of the source configuration Olivares AI runs against its own infrastructure, with every
credential replaced by a `REPLACE_WITH_…` placeholder or a `vault:` reference.

It ships because the shape is useful — it shows how several source kinds are declared together in
one file, which the four runnable examples do not — and it is listed here rather than in the
`## Runnable examples (with CI smoke tests)` table of [`../README.md`](../README.md), whose rows
each have a smoke test behind them.

Copy it, replace the placeholders, and point `olivares` at it. Nothing in it resolves as it stands.
