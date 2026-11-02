#!/usr/bin/env python3
"""Verify committed frontend assets against their pinned manifest."""
import hashlib
import json
from pathlib import Path

root = Path(__file__).resolve().parents[1] / "web" / "static" / "vendor"
manifest = json.loads((root / "manifest.json").read_text())
for asset in manifest:
    data = (root / asset["file"]).read_bytes()
    if hashlib.sha256(data).hexdigest() != asset["sha256"]:
        raise SystemExit("Asset checksum mismatch: " + asset["file"])
print(f"Verified {len(manifest)} pinned UI assets.")
