#!/usr/bin/env python3
"""Print the CHANGELOG.md section for one version as the release notes body.

Usage: changelog-notes.py <vX.Y.Z> [CHANGELOG.md]

The "## vX.Y.Z - date" heading itself is dropped (the app's What's new
dialog adds its own version heading); the "### Added/Fixed" subheadings
and bullets are kept as markdown. Exits 1 when the section is missing so a
release without notes is noticed.
"""
import re
import sys

tag = sys.argv[1].lstrip("v")
path = sys.argv[2] if len(sys.argv) > 2 else "CHANGELOG.md"
text = open(path, encoding="utf-8").read()
m = re.search(r"^## v" + re.escape(tag) + r"\b[^\n]*\n(.*?)(?=^## |\Z)", text, re.S | re.M)
if not m or not m.group(1).strip():
    sys.stderr.write(f"changelog-notes: no '## v{tag}' section in {path}\n")
    sys.exit(1)
print(m.group(1).strip())
