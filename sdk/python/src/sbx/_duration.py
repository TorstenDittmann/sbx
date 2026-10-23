from __future__ import annotations

import math
import re
from datetime import timedelta
from typing import Union

Duration = Union[int, float, str, timedelta]

_PATTERN = re.compile(r"^(\d+(?:\.\d+)?)(ms|s|m|h)?$")


def parse_duration(value: Duration) -> int:
    """Convert seconds, a timedelta, or a string such as "30s", "5m", "1h", or
    "500ms" into whole seconds. The server only accepts seconds."""
    if isinstance(value, bool):
        raise ValueError(f"invalid duration {value!r}")
    if isinstance(value, timedelta):
        value = value.total_seconds()
        if 0 < value < 1:
            return 1
    if isinstance(value, (int, float)):
        if not math.isfinite(value) or value < 0:
            raise ValueError(f"invalid duration {value!r}")
        return int(math.floor(value))
    if not isinstance(value, str):
        raise TypeError(f"invalid duration {value!r}")
    match = _PATTERN.match(value.strip())
    if not match:
        raise ValueError(f"invalid duration {value!r}")
    amount = float(match.group(1))
    unit = match.group(2) or "s"
    seconds = amount
    if unit == "ms":
        seconds = amount / 1000
    elif unit == "m":
        seconds = amount * 60
    elif unit == "h":
        seconds = amount * 3600
    if 0 < seconds < 1:
        return 1
    return int(math.floor(seconds))
