"""What may leave this platform, and what may not.

The agents send real claims to a model that runs on somebody else's computers.
Most of a claim should go: the crop, the extent, the damage, the dates, the
coordinates, the boundary, the photographs. That is the material the guidance is
built on and withholding it would leave the model guessing.

Two things must not go, and were named explicitly: the **farmer id** and the
**Aadhaar** hash or last four digits. The mobile number is withheld with them --
it serves nothing any agent does, and a contact number is the one field whose
leaking has an immediate consequence for the person. The farmer's **name** and the
parcel **boundary** are deliberately allowed.

# Why this is not a prompt instruction

Because a prompt is a request and this is a rule. Asking a model not to repeat an
identifier works until the day it does, and by then the identifier has already
been sent -- the leak happens on the way in, not on the way out. So nothing
reaches the transport without passing through here first.

# Why values are scrubbed as well as keys

Removing a `farmer_id` field does not remove a farmer id. Identifiers turn up
inside free text all the time: in an address line, in an officer's note, in a
caption somebody typed. A layer that only drops known keys is a layer that works
on the payloads it was written against and leaks on the first one it was not.

So the keys go, and then what remains is read for anything shaped like an
identifier: an eleven-digit AgriStack farmer id, a twelve-digit Aadhaar -- bare,
or grouped in fours the way the card prints it -- and a ten-digit Indian mobile.
Coordinates, areas and percentages carry decimal points and survey numbers are
short, so none of them collide with those shapes.

# Why it reports what it removed

So that this can be audited, and so that a redactor which starts eating something
the agents actually need is visible rather than mysterious. A silent filter is
indistinguishable from a bug.
"""

from __future__ import annotations

import re
from typing import Any

# Keys dropped outright, whatever they contain.
#
# Matched loosely -- case, underscores and hyphens removed -- because the same
# field arrives as `farmer_id`, `farmerId` and `FarmerID` depending on which
# layer produced it, and a boundary that depends on spelling is not a boundary.
FORBIDDEN_KEYS: frozenset[str] = frozenset(
    {
        "farmerid",
        "aadhaar",
        "aadhaarhash",
        "aadhaarlast4",
        "mobileno",
        "mobile",
        "mobilenumber",
        "phone",
        "phoneno",
    }
)

# Keys kept even though they are identifying. Named here so that allowing them is
# a decision on the record rather than an omission.
#
# The name is what makes a filled report usable by an officer, and the boundary is
# what the guidance reasons about. Both were allowed explicitly.
PERMITTED_IDENTIFYING_KEYS: frozenset[str] = frozenset(
    {
        "farmername",
        "name",
        "ownernameror",
        "geom",
        "geometry",
        "plotgeometry",
        "boundary",
    }
)

_REDACTED = "[redacted]"

# An AgriStack farmer id is eleven digits, an Aadhaar twelve, an Indian mobile ten
# beginning 6 to 9. Anchored on non-digits so a longer number -- a timestamp, an
# id with a checksum -- is left alone rather than half-eaten.
_IDENTIFIER_SHAPES: tuple[tuple[str, re.Pattern[str]], ...] = (
    ("aadhaar-shaped", re.compile(r"(?<!\d)\d{12}(?!\d)")),
    # Aadhaar as it is written on the card rather than as it is stored in a
    # field: four digits, a space or a hyphen, four more, again four. Added when
    # the officer's assistant began accepting uploaded spreadsheets, because a
    # sheet typed up in an office carries the number the way the card prints it,
    # and the bare twelve-digit shape above walks straight past that.
    #
    # A triple of four-digit years -- 2024-2025-2026 -- is caught by this too.
    # That is the direction to err in: a redacted year is a figure an officer can
    # ask about again in another form, and a leaked Aadhaar is not.
    ("aadhaar-shaped", re.compile(r"(?<!\d)\d{4}[ -]\d{4}[ -]\d{4}(?!\d)")),
    ("farmer-id-shaped", re.compile(r"(?<!\d)\d{11}(?!\d)")),
    ("mobile-shaped", re.compile(r"(?<!\d)[6-9]\d{9}(?!\d)")),
)


def _normalise(key: str) -> str:
    return key.replace("_", "").replace("-", "").replace(" ", "").lower()


def redact(value: Any) -> tuple[Any, list[str]]:
    """Strip what must not leave, and say what was stripped.

    Returns the cleaned value and a sorted list of what was removed, described by
    kind rather than by content -- a log of removals that quoted the identifiers
    would recreate the problem in the log file.
    """
    removed: set[str] = set()
    cleaned = _walk(value, removed, path="")
    return cleaned, sorted(removed)


def _walk(value: Any, removed: set[str], path: str) -> Any:
    if isinstance(value, dict):
        out: dict[str, Any] = {}
        for key, inner in value.items():
            norm = _normalise(str(key))
            here = f"{path}.{key}" if path else str(key)

            if norm in PERMITTED_IDENTIFYING_KEYS:
                # Allowed on purpose, and still walked: a boundary is geometry,
                # but a name field may carry a note and nested content is not
                # exempt from the value scrub below.
                out[key] = _walk(inner, removed, here)
                continue

            if norm in FORBIDDEN_KEYS:
                removed.add(here)
                continue

            out[key] = _walk(inner, removed, here)
        return out

    if isinstance(value, (list, tuple)):
        return [_walk(item, removed, f"{path}[]") for item in value]

    if isinstance(value, str):
        return _scrub(value, removed, path)

    return value


def _scrub(text: str, removed: set[str], path: str) -> str:
    out = text
    for kind, shape in _IDENTIFIER_SHAPES:
        if shape.search(out):
            removed.add(f"{path}:{kind}" if path else kind)
            out = shape.sub(_REDACTED, out)
    return out
