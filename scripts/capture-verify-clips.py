#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Play every delivered clip end to end and report what it actually contains.

Three answers, never two: 0 clean · 1 finding · 2 could not look.

"It rendered" is not "it plays": this decodes EVERY frame, so a GIF that is
truncated or has an undecodable tail fails here instead of in front of someone.
It also refuses a blank first frame — the frame every channel uses as the
thumbnail — and a file over the 5 MB budget.
"""
import os
import sys

from PIL import Image

LIMIT = 5 * 1024 * 1024


def blank(im):
    g = im.convert("L")
    lo, hi = g.getextrema()
    return hi - lo < 12


def check(path):
    findings = []
    size = os.path.getsize(path)
    im = Image.open(path)
    n, total = 0, 0.0
    first = None
    try:
        while True:
            im.seek(n)
            frame = im.convert("RGB")
            if n == 0:
                first = frame.copy()
            total += im.info.get("duration", 0) / 1000.0
            n += 1
    except EOFError:
        pass
    except Exception as exc:  # a frame that will not decode is exactly what this looks for
        return 2, [f"frame {n} did not decode: {exc}"], (n, total, size)

    if size > LIMIT:
        findings.append(f"{size/1024/1024:.2f} MB is over the 5 MB budget")
    if n < 2:
        findings.append(f"only {n} frame(s) — that is a still, not a clip")
    if first is None or blank(first):
        findings.append("first frame is blank — it is the thumbnail every channel shows")
    if total < 5:
        findings.append(f"total playtime {total:.1f}s is too short to read")
    return (1 if findings else 0), findings, (n, total, size)


def main():
    paths = sys.argv[1:]
    if not paths:
        print("verify-clips: nothing to check", file=sys.stderr)
        return 2
    worst = 0
    for p in paths:
        if not os.path.exists(p):
            print(f"⚠ NO HE PODIDO MIRAR  {p}: missing")
            worst = max(worst, 2)
            continue
        rc, findings, (n, total, size) = check(p)
        tag = {0: "ok   ", 1: "⛔ FALLO", 2: "⚠ CIEGO"}[rc]
        print(f"{tag} {os.path.basename(p)}: {n} frames · {total:.1f}s · {size/1024/1024:.2f} MB")
        for f in findings:
            print(f"        - {f}")
        worst = max(worst, rc)
    return worst


if __name__ == "__main__":
    sys.exit(main())
