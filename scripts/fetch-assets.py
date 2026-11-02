#!/usr/bin/env python3
"""Restore pinned assets without Node.js; never silently update their hashes."""
import hashlib
import json
from pathlib import Path
import urllib.request

root = Path(__file__).resolve().parents[1] / "web" / "static" / "vendor"
manifest = json.loads((root / "manifest.json").read_text())
for asset in manifest:
    target = root / asset["file"]
    if target.exists() and hashlib.sha256(target.read_bytes()).hexdigest() == asset["sha256"]:
        continue
    with urllib.request.urlopen(asset["url"], timeout=90) as response:
        data = response.read()
    if hashlib.sha256(data).hexdigest() != asset["sha256"]:
        raise SystemExit("Asset checksum mismatch: " + asset["file"])
    target.write_bytes(data)
    print("Restored", asset["file"])
