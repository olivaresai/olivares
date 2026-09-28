#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Bind an admission result to the consumer's explicit arguments and current job."""
import json
import os
import re
import sys


def attempt_id(value):
    if not isinstance(value, str) or not re.fullmatch(r"[0-9a-f]{32}", value):
        raise ValueError("invalid attempt identity")
    return value


def image_id(value):
    if not isinstance(value, str) or not re.fullmatch(r"sha256:[0-9a-f]{64}", value):
        raise ValueError("invalid immutable builder identity")
    return value


def same_job(recorded, context):
    """Refuse a record bound to another run, run attempt or job than the current one."""
    for key, variable in (("run_id", "GITHUB_RUN_ID"), ("run_attempt", "GITHUB_RUN_ATTEMPT"), ("job", "GITHUB_JOB")):
        if not context.get(variable) or recorded.get(key) != context[variable]:
            raise ValueError("bound to another run, attempt or job: " + key)


def check(reply, attempt, image, accelerator, context):
    attempt_id(attempt)
    image_id(image)
    if accelerator not in ("kvm", "tcg"):
        raise ValueError("explicit kvm or tcg accelerator required")
    if not isinstance(reply, dict) or reply.get("state") != "admitted":
        raise ValueError("builder was not admitted")
    expected = reply.get("expected_context")
    if not isinstance(expected, dict):
        raise ValueError("admitted context missing")
    for key, value in (("image_id", image), ("accelerator", accelerator)):
        if reply.get(key) != value or expected.get(key) != value:
            raise ValueError("admission differs: " + key)
    if expected.get("attempt_id") != attempt:
        raise ValueError("admission differs: attempt")
    same_job(expected, context)
    return reply


if __name__ == "__main__":
    try:
        if len(sys.argv) != 4:
            raise ValueError("attempt, image and accelerator required")
        check(json.load(sys.stdin), *sys.argv[1:], os.environ)
    except (ValueError, TypeError, OSError) as error:
        print("recipe admission refused: " + str(error), file=sys.stderr)
        sys.exit(1)
