#!/usr/bin/env python3
"""Print the CHANGELOG.md section for one version as the release notes body.

Usage: changelog-notes.py <vX.Y.Z> [CHANGELOG.md]

The "## vX.Y.Z - date" heading itself is dropped (the app's What's new
dialog adds its own version heading); the "### Added/Fixed" subheadings
and bullets are kept as markdown. A release minted at merge time has no
"## vX.Y.Z" section yet (the version is only decided then), so the
"## Unreleased" section stands in for it. Exits 1 when neither has content,
so a release without notes is noticed.
"""
import re
import sys

tag = sys.argv[1].lstrip("v")
path = sys.argv[2] if len(sys.argv) > 2 else "CHANGELOG.md"
text = open(path, encoding="utf-8").read()


def section(heading):
    m = re.search(r"^## " + heading + r"\b[^\n]*\n(.*?)(?=^## |\Z)", text, re.S | re.M)
    return m.group(1).strip() if m else ""


body = section("v" + re.escape(tag)) or section("Unreleased")
if not body:
    sys.stderr.write(f"changelog-notes: no '## v{tag}' or '## Unreleased' section with content in {path}\n")
    sys.exit(1)
print(body)
