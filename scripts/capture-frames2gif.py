#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Assemble CDP screencast frames into an animated GIF.

Consumes the frames_dir that capture-console-rec.mjs writes (frames.json + PNGs).

Frame durations come from the recorded timestamps, so the GIF plays at the pace
the page actually rendered at. Trailing hold is added so the last state is
readable; a leading hold makes the first frame — the one every channel uses as
the thumbnail — stay up long enough to read.
"""
import argparse
import json
import os
import sys

from PIL import Image


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("frames_dir")
    ap.add_argument("out")
    ap.add_argument("--scale", type=float, default=1.0)
    ap.add_argument("--colors", type=int, default=128)
    ap.add_argument("--hold-first", type=float, default=1.2)
    ap.add_argument("--hold-last", type=float, default=2.5)
    ap.add_argument("--min-frame", type=float, default=0.04)
    ap.add_argument("--max-frame", type=float, default=3.0)
    ap.add_argument("--crop", default="", help="left,top,right,bottom")
    args = ap.parse_args()

    meta = json.load(open(os.path.join(args.frames_dir, "frames.json")))
    fr = meta["frames"]
    if not fr:
        print("frames2gif: no frames", file=sys.stderr)
        return 2

    imgs, durs = [], []
    for i, f in enumerate(fr):
        nxt = fr[i + 1]["t"] if i + 1 < len(fr) else fr[i]["t"] + int(args.hold_last * 1000)
        d = max(args.min_frame, min(args.max_frame, (nxt - f["t"]) / 1000.0))
        im = Image.open(os.path.join(args.frames_dir, f["file"])).convert("RGB")
        if args.crop:
            im = im.crop(tuple(int(x) for x in args.crop.split(",")))
        if args.scale != 1.0:
            im = im.resize(
                (int(im.width * args.scale), int(im.height * args.scale)), Image.LANCZOS
            )
        imgs.append(im)
        durs.append(d)

    durs[0] += args.hold_first
    durs[-1] += args.hold_last

    quant = [
        im.quantize(colors=args.colors, method=Image.MEDIANCUT, dither=Image.NONE) for im in imgs
    ]
    quant[0].save(
        args.out,
        save_all=True,
        append_images=quant[1:],
        duration=[max(20, int(d * 1000)) for d in durs],
        loop=0,
        optimize=True,
        disposal=1,
    )
    size = os.path.getsize(args.out)
    print(
        f"frames2gif: {args.out} · {len(imgs)} frames · {sum(durs):.1f}s · "
        f"{size/1024/1024:.2f} MB · {imgs[0].size[0]}x{imgs[0].size[1]}",
        file=sys.stderr,
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
