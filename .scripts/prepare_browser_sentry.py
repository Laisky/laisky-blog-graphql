#!/usr/bin/env python3
"""Prepare only validated public MCP browser build arguments for GitHub Actions."""

import os
from pathlib import Path
import re
import sys

FIELDS = (
    "VITE_SENTRY_ENABLED", "VITE_SENTRY_SITE", "VITE_SENTRY_DSN",
    "VITE_SENTRY_ENVIRONMENT", "VITE_SENTRY_RELEASE",
)
INVALID = "Invalid MCP browser reporting build configuration."
DSN = re.compile(r"https://[a-f0-9]{32}@o[0-9]+\.ingest(?:\.[a-z]+)?\.sentry\.io/[0-9]{1,20}")


class InvalidConfiguration(ValueError):
    """InvalidConfiguration signals invalid explicit enablement without retaining input values."""


def prepare_arguments(enabled, dsn, environment, source_sha):
    """prepare_arguments returns five bounded public values, blank when off, or raises on invalid opt-in."""
    values = dict.fromkeys(FIELDS, "")
    if enabled != "true":
        return values
    if (
        not isinstance(dsn, str) or len(dsn) > 256 or not DSN.fullmatch(dsn)
        or environment not in ("development", "staging", "production")
        or not isinstance(source_sha, str) or not re.fullmatch(r"[a-f0-9]{40}", source_sha)
    ):
        raise InvalidConfiguration(INVALID)
    values.update(
        VITE_SENTRY_ENABLED="true", VITE_SENTRY_SITE="mcp", VITE_SENTRY_DSN=dsn,
        VITE_SENTRY_ENVIRONMENT=environment, VITE_SENTRY_RELEASE=source_sha,
    )
    return values


def output_record(values):
    """output_record returns a deterministic Actions multiline output containing exactly five arguments."""
    arguments = "\n".join(name + "=" + values[name] for name in FIELDS)
    return "build_args<<MCP_BROWSER_ARGS\n" + arguments + "\nMCP_BROWSER_ARGS\n"


def main(argv=None):
    """main writes validated public arguments to the supplied runner output file without echoing values."""
    arguments = sys.argv[1:] if argv is None else argv
    if len(arguments) != 2 or arguments[0] != "--github-output":
        print(INVALID, file=sys.stderr)
        return 2
    try:
        # Only the three approved public repository variables and trusted workflow SHA are read.
        values = prepare_arguments(
            os.environ.get("MCP_SENTRY_ENABLED", ""), os.environ.get("MCP_SENTRY_DSN", ""),
            os.environ.get("MCP_SENTRY_ENVIRONMENT", ""), os.environ.get("SOURCE_SHA", ""),
        )
        with Path(arguments[1]).open("a", encoding="utf-8") as output:
            output.write(output_record(values))
    except (InvalidConfiguration, OSError, ValueError):
        print(INVALID, file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
