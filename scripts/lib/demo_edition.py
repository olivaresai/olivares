# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Which edition the demo engine runs, and what the demo seeders may therefore create.

A demo estate shows what its edition does. Community answers 501 to the Business routes and its
console has no page for them, so a Community estate must hold no object that only a Business page
can act on: a deployment promotion or an audit evidence export waiting for approval, a deployment
or a red-team target. The seeders read the edition once, from the engine, and ask this module.

The lists below are the one place the seeders learn what docs/editions.md places in Business.
"""

# Approval kinds whose action only a Business page performs (deployment, evidence bundles).
BUSINESS_APPROVAL_ACTIONS = frozenset({"deploy.promote", "evidence.export"})
# Volume surfaces (scripts/seed-estate-volume.py ids) that exist only in Business.
BUSINESS_SURFACES = frozenset({"deploy", "redteam"})
KNOWN_EDITIONS = frozenset({"community", "enterprise", "business"})


def edition_of(code, info):
    """The edition a GET /v1/server-info answer names, or raise: guessing seeds the wrong estate."""
    edition = info.get("edition") if isinstance(info, dict) else None
    if code != 200 or edition not in KNOWN_EDITIONS:
        raise ValueError("cannot tell the edition from /v1/server-info (HTTP %s, edition %r)" % (code, edition))
    return edition


def keeps_action(edition, action):
    """Whether an approval of `action` belongs in an estate of `edition`. No edition keeps everything."""
    return edition != "community" or action not in BUSINESS_APPROVAL_ACTIONS


def keeps_surface(edition, surface):
    return edition != "community" or surface not in BUSINESS_SURFACES
