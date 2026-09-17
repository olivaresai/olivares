#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Render an asciicast v2 file to an animated GIF.

Consumes what capture-ptydrive.py records.

Uses pyte (a VT emulator) to replay the recorded bytes into a screen buffer, and
Pillow to draw each distinct screen state. No text is invented here: the only
inputs are the recorded bytes.

Playback conventions (declared, not hidden):
  --idle-cap S   long gaps between events are clamped to S seconds (asciinema's
                 --idle-time-limit). It changes PACING, never content.
  --speed X      uniform time scaling.
"""
import argparse
import json
import sys

import pyte
from PIL import Image, ImageDraw, ImageFont

MONO = "/usr/share/fonts/truetype/liberation/LiberationMono-Regular.ttf"
MONO_BOLD = "/usr/share/fonts/truetype/liberation/LiberationMono-Bold.ttf"

THEMES = {
    "dark": {
        "bg": (13, 17, 23),
        "fg": (201, 209, 217),
        "cursor": (88, 166, 255),
        "chrome": (22, 27, 34),
        "chrome_fg": (125, 133, 144),
        "black": (48, 54, 61),
        "red": (255, 123, 114),
        "green": (63, 185, 80),
        "brown": (210, 153, 34),
        "yellow": (210, 153, 34),
        "blue": (88, 166, 255),
        "magenta": (188, 140, 255),
        "cyan": (57, 197, 207),
        "white": (201, 209, 217),
    },
    "light": {
        "bg": (255, 255, 255),
        "fg": (31, 35, 40),
        "cursor": (9, 105, 218),
        "chrome": (246, 248, 250),
        "chrome_fg": (101, 109, 118),
        "black": (31, 35, 40),
        "red": (207, 34, 46),
        "green": (26, 127, 55),
        "brown": (154, 103, 0),
        "yellow": (154, 103, 0),
        "blue": (9, 105, 218),
        "magenta": (130, 80, 223),
        "cyan": (23, 122, 133),
        "white": (110, 119, 129),
    },
}


def resolve_colour(name, theme, default_key):
    if not name or name == "default":
        return theme[default_key]
    if name in theme:
        return theme[name]
    # pyte hands back a bare hex string for 256-colour / truecolour SGR.
    if len(name) == 6:
        try:
            return (int(name[0:2], 16), int(name[2:4], 16), int(name[4:6], 16))
        except ValueError:
            pass
    return theme[default_key]


def load_cast(path):
    with open(path, encoding="utf-8") as fh:
        header = json.loads(fh.readline())
        events = []
        for line in fh:
            line = line.strip()
            if not line:
                continue
            ev = json.loads(line)
            if len(ev) >= 3 and ev[1] == "o":
                events.append((float(ev[0]), ev[2]))
    return header, events


def screen_signature(screen):
    rows = []
    for y in range(screen.lines):
        row = screen.buffer[y]
        rows.append(
            tuple(
                (row[x].data, row[x].fg, row[x].bg, row[x].bold, row[x].reverse)
                for x in range(screen.columns)
            )
        )
    return (tuple(rows), screen.cursor.x, screen.cursor.y, screen.cursor.hidden)


def draw_screen(screen, theme, font, font_bold, cw, ch, pad, header_text, header_h):
    w = screen.columns * cw + 2 * pad
    h = screen.lines * ch + 2 * pad + header_h
    img = Image.new("RGB", (w, h), theme["bg"])
    d = ImageDraw.Draw(img)

    if header_h:
        d.rectangle([0, 0, w, header_h], fill=theme["chrome"])
        for i, (cx, col) in enumerate(
            ((14, (255, 95, 86)), (32, (255, 189, 46)), (50, (39, 201, 63)))
        ):
            d.ellipse([cx - 5, header_h // 2 - 5, cx + 5, header_h // 2 + 5], fill=col)
        if header_text:
            hf = ImageFont.truetype(MONO, max(11, int(ch * 0.62)))
            bbox = d.textbbox((0, 0), header_text, font=hf)
            d.text(
                ((w - (bbox[2] - bbox[0])) / 2, (header_h - (bbox[3] - bbox[1])) / 2 - 2),
                header_text,
                font=hf,
                fill=theme["chrome_fg"],
            )

    y0 = header_h + pad
    for y in range(screen.lines):
        row = screen.buffer[y]
        for x in range(screen.columns):
            cell = row[x]
            ch_data = cell.data
            fg = resolve_colour(cell.fg, theme, "fg")
            bg = resolve_colour(cell.bg, theme, "bg")
            if cell.reverse:
                fg, bg = bg, fg
            px, py = pad + x * cw, y0 + y * ch
            if bg != theme["bg"]:
                d.rectangle([px, py, px + cw, py + ch], fill=bg)
            if ch_data and ch_data != " ":
                d.text((px, py), ch_data, font=font_bold if cell.bold else font, fill=fg)

    if not screen.cursor.hidden and screen.cursor.y < screen.lines:
        px = pad + screen.cursor.x * cw
        py = y0 + screen.cursor.y * ch
        d.rectangle([px, py, px + cw - 1, py + ch - 1], fill=theme["cursor"])
        cursor_cell = screen.buffer[screen.cursor.y][screen.cursor.x]
        if cursor_cell.data and cursor_cell.data != " ":
            d.text((px, py), cursor_cell.data, font=font, fill=theme["bg"])
    return img


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("cast")
    ap.add_argument("out")
    ap.add_argument("--theme", choices=list(THEMES), default="dark")
    ap.add_argument("--font-size", type=int, default=15)
    ap.add_argument("--fps", type=float, default=10.0)
    ap.add_argument("--idle-cap", type=float, default=1.2)
    ap.add_argument("--speed", type=float, default=1.0)
    ap.add_argument("--hold-first", type=float, default=1.6)
    ap.add_argument("--hold-last", type=float, default=2.5)
    ap.add_argument("--colors", type=int, default=64)
    ap.add_argument("--title", default="")
    ap.add_argument("--frames-dir", default="")
    args = ap.parse_args()

    theme = THEMES[args.theme]
    header, events = load_cast(args.cast)
    cols, rows = header["width"], header["height"]

    font = ImageFont.truetype(MONO, args.font_size)
    font_bold = ImageFont.truetype(MONO_BOLD, args.font_size)
    probe = ImageDraw.Draw(Image.new("RGB", (10, 10)))
    cw = int(round(probe.textlength("M" * 20, font=font) / 20))
    asc, desc = font.getmetrics()
    ch = asc + desc + 2
    pad = 12
    header_h = 30 if args.title else 0

    # Re-time: clamp idle gaps, then scale.
    timed, clock, prev = [], 0.0, 0.0
    for t, data in events:
        gap = min(t - prev, args.idle_cap)
        clock += max(gap, 0.0)
        prev = t
        timed.append((clock / args.speed, data))
    total = timed[-1][0] if timed else 0.0

    screen = pyte.Screen(cols, rows)
    stream = pyte.Stream(screen)

    step = 1.0 / args.fps
    frames, durations = [], []
    last_sig = None
    idx = 0
    sample = 0.0
    n_samples = int(total / step) + 2

    def screen_is_blank(scr):
        return not any(
            scr.buffer[y][x].data.strip() for y in range(scr.lines) for x in range(scr.columns)
        )

    for _ in range(n_samples):
        while idx < len(timed) and timed[idx][0] <= sample:
            stream.feed(timed[idx][1])
            idx += 1
        # A GIF whose first frame is empty fails the "legible first frame" rule: the
        # thumbnail every channel shows is frame 0. Leading blank screens are dropped,
        # not held.
        if not frames and screen_is_blank(screen):
            sample += step
            continue
        sig = screen_signature(screen)
        if sig != last_sig:
            frames.append(
                draw_screen(screen, theme, font, font_bold, cw, ch, pad, args.title, header_h)
            )
            durations.append(step)
            last_sig = sig
        else:
            durations[-1] += step
        sample += step

    while idx < len(timed):
        stream.feed(timed[idx][1])
        idx += 1
    sig = screen_signature(screen)
    if sig != last_sig:
        frames.append(
            draw_screen(screen, theme, font, font_bold, cw, ch, pad, args.title, header_h)
        )
        durations.append(step)

    if not frames:
        print("castrender: nothing to render", file=sys.stderr)
        return 2

    durations[0] += args.hold_first
    durations[-1] += args.hold_last

    if args.frames_dir:
        import os

        os.makedirs(args.frames_dir, exist_ok=True)
        for i, fr in enumerate(frames):
            fr.save(f"{args.frames_dir}/frame-{i:04d}.png")

    quant = [
        f.convert("RGB").quantize(colors=args.colors, method=Image.MEDIANCUT, dither=Image.NONE)
        for f in frames
    ]
    quant[0].save(
        args.out,
        save_all=True,
        append_images=quant[1:],
        duration=[max(20, int(d * 1000)) for d in durations],
        loop=0,
        optimize=True,
        disposal=1,
    )
    import os

    size = os.path.getsize(args.out)
    print(
        f"castrender: {args.out} · {len(frames)} frames · {sum(durations):.1f}s · "
        f"{size/1024/1024:.2f} MB · {frames[0].size[0]}x{frames[0].size[1]}",
        file=sys.stderr,
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
