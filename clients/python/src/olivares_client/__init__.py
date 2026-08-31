# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: Apache-2.0
"""First-party Python client for the Olivares AI control plane REST API (/v1).

Two layers:

- a hand-written core (:mod:`olivares_client._core`): auth, tenancy, the error
  envelope (:class:`APIError`), cursor pagination, Retry-After-aware retries
  and deprecation signaling (:class:`DeprecationNotice`);
- a generated operation layer (:mod:`olivares_client._operations`), regenerated
  from the committed OpenAPI snapshot by ``task sdk:generate`` and
  drift-checked by ``task sdk:check``.

Versioning: ``API_VERSION`` is the API contract major the operation layer was
generated from; ``__version__`` is the SDK's own version, whose MAJOR tracks
the API major from GA on. Governing policy: ``STABILITY_POLICY``
(https://olivares.ai/docs).

Example::

    from olivares_client import Client

    c = Client("https://olivares.example:8443", token="olvk_...")
    for agent in c.paginate("/v1/agents"):
        print(agent["id"])
"""

from ._core import VERSION as __version__
from ._core import APIError, ClientCore, DeprecationNotice
from ._operations import API_VERSION, SPEC_HASH, STABILITY_POLICY, OperationsMixin


class Client(OperationsMixin, ClientCore):
    """Control-plane API client: the generated operation methods on top of the
    transport core. See the package docstring for the layering."""


__all__ = [
    "APIError",
    "API_VERSION",
    "Client",
    "ClientCore",
    "DeprecationNotice",
    "OperationsMixin",
    "SPEC_HASH",
    "STABILITY_POLICY",
    "__version__",
]
