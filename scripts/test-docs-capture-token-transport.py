#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Check capture helpers' token parsing at the real HTTP request boundary, without networking."""

import contextlib
import importlib.util
import io
import os
import sys
import unittest
from pathlib import Path
from unittest.mock import patch
from urllib.request import Request


class RequestReached(BaseException):
    """Stop before HTTP, including helpers that catch ordinary network exceptions."""


class CaptureTokenTransport(unittest.TestCase):
    helpers = ("seed-demo-work.py", "seed-estate-volume.py", "seed-adoption-otlp.py")

    def invoke(
        self, filename: str, token_arg: str, environment: dict[str, str]
    ) -> tuple[list[str | None], int | None, list[str]]:
        path = Path(__file__).resolve().with_name(filename)
        spec = importlib.util.spec_from_file_location("capture_token_helper", path)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        argv = [str(path), "http://capture.invalid", token_arg, "capture-tenant"]
        if filename == "seed-demo-work.py":
            argv.append("/unused/capture-workspace")
        observed = []

        def stop(request: Request, **kwargs: object) -> None:
            observed.append(request.get_header("Authorization"))
            raise RequestReached()

        with contextlib.ExitStack() as stack:
            stack.enter_context(patch.dict(os.environ, environment, clear=True))
            stack.enter_context(patch.object(sys, "argv", argv))
            stack.enter_context(patch.object(sys, "excepthook", sys.excepthook))
            stack.enter_context(patch.object(module, "abre", stop))
            stack.enter_context(contextlib.redirect_stdout(io.StringIO()))
            stack.enter_context(contextlib.redirect_stderr(io.StringIO()))
            if filename == "seed-estate-volume.py":
                # Catalogue input is unrelated to credential parsing; keep the request real.
                stack.enter_context(
                    patch.object(module, "carga_objetivos", return_value=({}, [], None))
                )
            try:
                if filename == "seed-estate-volume.py":
                    module.main(argv)
                else:
                    module.main()
            except RequestReached:
                return observed, None, argv
            except SystemExit as error:
                return observed, error.code, argv
        self.fail("helper returned before either the request or parser refusal")

    def test_environment_token_reaches_request_without_token_in_arguments(self) -> None:
        for filename in self.helpers:
            with self.subTest(helper=filename):
                observed, status, argv = self.invoke(
                    filename, "-", {"OLIVARES_TOKEN": "synthetic-env-marker"}
                )
                self.assertEqual(observed, ["Bearer synthetic-env-marker"])
                self.assertIsNone(status)
                self.assertNotIn("synthetic-env-marker", argv)

    def test_existing_positional_token_keeps_precedence(self) -> None:
        for filename in self.helpers:
            with self.subTest(helper=filename):
                observed, status, _ = self.invoke(
                    filename, "synthetic-positional-marker",
                    {"OLIVARES_TOKEN": "synthetic-env-marker"},
                )
                self.assertEqual(observed, ["Bearer synthetic-positional-marker"])
                self.assertIsNone(status)

    def test_environment_token_is_required_before_any_request(self) -> None:
        for filename in self.helpers:
            with self.subTest(helper=filename):
                observed, status, _ = self.invoke(filename, "-", {})
                self.assertEqual((observed, status), ([], 2))


if __name__ == "__main__":
    unittest.main(verbosity=2)
