# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: Apache-2.0
"""Transport core for the Olivares AI control plane client.

The hand-written half of the SDK: endpoint/auth/tenancy wiring for the opaque
bearer tokens (olvs_/olvk_), the single error envelope mapped to
:class:`APIError`, cursor pagination (:meth:`ClientCore.paginate`),
Retry-After-aware retries for rate-limited calls, and surfacing of the
stability policy's deprecation signal (RFC 9745 ``Deprecation`` / RFC 8594
``Sunset`` response headers), once per endpoint.

Standard library only — the SDK never links the engine.
"""

from __future__ import annotations

import json
import ssl
import threading
import time
import warnings
from dataclasses import dataclass
from urllib import error as _urlerror
from urllib import request as _urlrequest
from urllib.parse import urlencode

from ._operations import API_VERSION


class _NoRedirects(_urlrequest.HTTPRedirectHandler):
    """Refuse redirects outright. A control plane does not 3xx its JSON
    endpoints, and urllib's default handler would forward the bearer token and
    tenant header cross-origin AND silently convert a redirected POST into a
    body-less GET — both unacceptable for a credentialed API client. The 3xx
    surfaces as an :class:`APIError` (code ``http_3xx``) instead."""

    def redirect_request(self, req, fp, code, msg, headers, newurl):  # noqa: D102
        return None

#: The SDK's own semantic version. Pre-1.0 while the product is pre-1.0 (the
#: policy's support windows bind from GA); from GA on, the MAJOR tracks the API
#: contract major (``API_VERSION``).
VERSION = "0.1.0"

_RETRYABLE_GET_ONLY = {503}  # not_leader HA handoff — idempotent reads only
_BODY_LIMIT = 64 << 20


@dataclass(frozen=True)
class DeprecationNotice:
    """One deprecated-endpoint signal, parsed from the policy's headers."""

    method: str
    path: str  #: the request path (concrete, not the route template)
    deprecation: str  #: raw ``Deprecation`` value, e.g. ``@1780272000``
    sunset: str  #: raw ``Sunset`` value (HTTP-date), ``""`` if unscheduled
    link: str  #: migration-guide URL from ``Link rel="deprecation"``, if any


class APIError(Exception):
    """The API's single error envelope (``{"error":{code,message}}``) plus
    transport context. ``code`` values are part of the stable contract."""

    def __init__(self, status: int, code: str, message: str, request_id: str = ""):
        super().__init__(f"{message} ({code} {status}, request {request_id})")
        self.status = status
        self.code = code
        self.message = message
        self.request_id = request_id


def _deprecation_link(link_headers: list[str]) -> str:
    """Extract the ``rel="deprecation"`` target from Link header values."""
    for raw in link_headers:
        for part in raw.split(","):
            if 'rel="deprecation"' not in part:
                continue
            i, j = part.find("<"), part.find(">")
            if 0 <= i < j:
                return part[i + 1 : j]
    return ""


class ClientCore:
    """Transport; the generated ``OperationsMixin`` adds one method per
    published operation. Use :class:`olivares_client.Client`."""

    def __init__(
        self,
        endpoint: str,
        token: str = "",
        *,
        tenant: str | None = None,
        verify: bool = True,
        max_retries: int = 2,
        timeout: float = 30.0,
        user_agent: str | None = None,
        on_deprecation=None,
        _sleep=time.sleep,
    ):
        if "://" not in endpoint:
            raise ValueError(f"endpoint must be an absolute URL: {endpoint!r}")
        self._endpoint = endpoint.rstrip("/")
        self._token = token
        self._tenant = tenant
        self._max_retries = max_retries
        self._timeout = timeout
        self._on_deprecation = on_deprecation
        self._sleep = _sleep
        self._dep_seen: set[str] = set()
        self._dep_lock = threading.Lock()  # a Client may be shared across threads
        ua = f"olivares-client-python/{VERSION} (api {API_VERSION})"
        self._user_agent = f"{user_agent} {ua}" if user_agent else ua
        self._ssl_ctx = None
        if not verify:
            # The engine is TLS-by-default with a self-signed certificate out of
            # the box — labs need the escape hatch; production should pin
            # a real CA instead.
            self._ssl_ctx = ssl.create_default_context()
            self._ssl_ctx.check_hostname = False
            self._ssl_ctx.verify_mode = ssl.CERT_NONE
        # Per-client opener: refuses redirects (see _NoRedirects) and carries
        # the TLS context (OpenerDirector.open has no context kwarg).
        handlers = [_NoRedirects()]
        if self._ssl_ctx is not None:
            handlers.append(_urlrequest.HTTPSHandler(context=self._ssl_ctx))
        self._opener = _urlrequest.build_opener(*handlers)

    # -- request execution -----------------------------------------------------

    def _do(self, method: str, route: str, path: str, *, body=None, query=None,
            tenant=None, raw_request=False, raw_request_content_type=None,
            _required_json_body=False):
        """One JSON operation. ``route`` is the spec path template (the
        deprecation-dedup key, e.g. ``/v1/agents/{id}``); ``path`` is the
        concrete escaped request path. ``raw_request_content_type`` sends
        ``body`` as raw bytes under the contract's exact media type."""
        raw = self._execute(method, route, path, body=body, query=query,
                            tenant=tenant, raw_request=raw_request,
                            raw_request_content_type=raw_request_content_type,
                            required_json_body=_required_json_body)
        if not raw.strip():
            return {}
        try:
            out = json.loads(raw)
        except ValueError:
            out = None
        if not isinstance(out, dict):
            raise APIError(0, "bad_response", f"response is not a JSON object ({method} {path})")
        return out

    def _do_json_required(self, method: str, route: str, path: str, *, body,
                          query=None, tenant=None):
        """Execute a required JSON request. ``None`` is the present JSON value
        ``null``; optional and legacy calls through :meth:`_do` still omit it."""
        return self._do(method, route, path, body=body, query=query, tenant=tenant,
                        _required_json_body=True)

    def _do_raw(self, method: str, route: str, path: str, *, query=None, tenant=None) -> bytes:
        """An operation whose success body is NOT JSON (e.g. /metrics); errors
        still arrive in the JSON envelope and raise :class:`APIError`."""
        return self._execute(method, route, path, body=None, query=query,
                             tenant=tenant, want_json=False)

    def _execute(self, method, route, path, *, body, query, tenant, want_json=True,
                 raw_request=False, raw_request_content_type=None,
                 required_json_body=False) -> bytes:
        """The policy-aware retry loop: 429 is always retryable (the limiter
        rejects before execution and ``Retry-After`` is a safe lower bound),
        503 only for GET. Everything else surfaces."""
        attempt = 0
        while True:
            try:
                return self._once(method, route, path, body=body, query=query,
                                  tenant=tenant, want_json=want_json,
                                  raw_request=raw_request,
                                  raw_request_content_type=raw_request_content_type,
                                  required_json_body=required_json_body)
            except APIError as e:
                retryable = e.status == 429 or (
                    e.status in _RETRYABLE_GET_ONLY and method == "GET"
                )
                if not retryable or attempt >= self._max_retries:
                    raise
                wait = getattr(e, "_retry_after", 0.0) or (attempt + 1) * 0.5
                attempt += 1
                self._sleep(wait)

    def _once(self, method, route, path, *, body, query, tenant, want_json,
              raw_request=False, raw_request_content_type=None,
              required_json_body=False) -> bytes:
        url = self._endpoint + path
        q = {k: v for k, v in (query or {}).items() if v is not None}
        if q:
            # doseq: a list/tuple value is a REPEATED parameter, one occurrence per
            # entry. Without it urlencode writes the Python repr of the list as a
            # single value ("exclude_action=%5B%27a%27%2C+%27b%27%5D"), which no
            # server can read back. /v1/audit's exclude_action is the API's first
            # repeatable query parameter; it will not be the last.
            url += "?" + urlencode(q, doseq=True)
        data = None
        headers = {"User-Agent": self._user_agent}
        if want_json:
            headers["Accept"] = "application/json"
        if raw_request_content_type is not None:
            if body is not None:
                data = bytes(body)
                headers["Content-Type"] = raw_request_content_type
        elif raw_request:
            # Compatibility with operation layers generated before media types
            # were propagated explicitly; those callers were octet-stream.
            if body is not None:
                data = bytes(body)
                headers["Content-Type"] = "application/octet-stream"
        elif required_json_body or body is not None:
            data = json.dumps(body).encode()
            headers["Content-Type"] = "application/json"
        if self._token:
            headers["Authorization"] = f"Bearer {self._token}"
        t = tenant or self._tenant
        if t:
            headers["X-Olivares-Tenant"] = t

        req = _urlrequest.Request(url, data=data, method=method, headers=headers)
        try:
            with self._opener.open(req, timeout=self._timeout) as resp:
                self._notice_deprecation(method, route, path, resp.headers)
                return resp.read(_BODY_LIMIT)
        except _urlerror.HTTPError as e:
            self._notice_deprecation(method, route, path, e.headers)
            raise self._api_error(e) from None

    @staticmethod
    def _api_error(e: _urlerror.HTTPError) -> APIError:
        raw = e.read(_BODY_LIMIT)
        code, message = f"http_{e.code}", raw.decode(errors="replace").strip()
        try:
            envelope = json.loads(raw)["error"]
            code, message = envelope["code"], envelope.get("message", "")
        except (ValueError, KeyError, TypeError):
            pass
        err = APIError(e.code, code, message, e.headers.get("X-Request-ID", ""))
        retry_after = e.headers.get("Retry-After", "")
        # isascii() too: isdigit() alone accepts Unicode digits ('²', '１２３')
        # that float() rejects or that Go/TS would not parse — non-ASCII forms
        # fall back to 0.0 instead of raising out of the error path.
        err._retry_after = (
            float(retry_after) if retry_after.isascii() and retry_after.isdigit() else 0.0
        )
        return err

    # -- stability policy signal -------------------------------------------------

    def _notice_deprecation(self, method, route, path, headers) -> None:
        dep = headers.get("Deprecation", "")
        if not dep:
            return
        # Dedup per ENDPOINT (the route template, matching the server-side
        # declaration): a deprecated /v1/agents/{id} warns once, not once per
        # agent, and the set stays bounded by the published surface. The lock
        # makes the check-then-add safe for a Client shared across threads.
        key = f"{method} {route}"
        with self._dep_lock:
            if key in self._dep_seen:
                return
            self._dep_seen.add(key)
        notice = DeprecationNotice(
            method=method,
            path=path,
            deprecation=dep,
            sunset=headers.get("Sunset", ""),
            link=_deprecation_link(headers.get_all("Link") or []),
        )
        if self._on_deprecation is not None:
            self._on_deprecation(notice)
            return
        warnings.warn(
            f"olivares: {method} {path} is deprecated ({notice.deprecation}"
            + (f", sunset {notice.sunset}" if notice.sunset else "")
            + (f"); see {notice.link}" if notice.link else ")"),
            DeprecationWarning,
            stacklevel=4,
        )

    # -- pagination ----------------------------------------------------------------

    def paginate(self, path: str, *, tenant: str | None = None, **query):
        """Iterate a cursor-paginated collection endpoint (the
        ``items``/``cursor``/``has_more`` envelope), yielding each item."""
        cursor = None
        while True:
            q = dict(query)
            if cursor:
                q["cursor"] = cursor
            page = self._do("GET", path, path, query=q, tenant=tenant)
            yield from page.get("items") or []
            cursor = page.get("cursor")
            if not page.get("has_more") or not cursor:
                return
