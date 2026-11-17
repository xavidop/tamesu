#!/usr/bin/env python3
"""Update the pinned versions of the external CLIs tamesu drives.

internal/pinned/pins.json names, for agent-browser and agent-desktop, the
release tamesu downloads and the SHA-256 of each platform's file. This script
moves a pin to the newest release that is old enough (a release has to have
been public for --min-age-days before tamesu adopts it) and rewrites the file.

    update_cli_pins.py                      # both tools, 5 days
    update_cli_pins.py --tool agent-desktop --min-age-days 0

It prints one line per tool and, under GitHub Actions, sets the outputs
`changed`, `old` and `new` (of the last tool that moved). A pin never moves
backwards, and a release that lacks a file for one of the platforms is skipped
with an error rather than pinned in part.
"""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import os
import re
import sys
import urllib.request
from pathlib import Path

API = "https://api.github.com"

# Each tool's repository and, per tamesu platform (GOOS/GOARCH, with
# linux-musl for a musl libc), the name of its release file. {v} is the
# version without the leading v.
TOOLS = {
    "agent-browser": {
        "repo": "vercel-labs/agent-browser",
        "assets": {
            "darwin/amd64": "agent-browser-darwin-x64",
            "darwin/arm64": "agent-browser-darwin-arm64",
            "linux/amd64": "agent-browser-linux-x64",
            "linux/arm64": "agent-browser-linux-arm64",
            "linux-musl/amd64": "agent-browser-linux-musl-x64",
            "linux-musl/arm64": "agent-browser-linux-musl-arm64",
            "windows/amd64": "agent-browser-win32-x64.exe",
        },
    },
    "agent-desktop": {
        "repo": "vercel-labs/agent-desktop",
        "assets": {
            "darwin/amd64": "agent-desktop-v{v}-x86_64-apple-darwin.tar.gz",
            "darwin/arm64": "agent-desktop-v{v}-aarch64-apple-darwin.tar.gz",
            "linux/amd64": "agent-desktop-v{v}-x86_64-unknown-linux-gnu.tar.gz",
            "linux/arm64": "agent-desktop-v{v}-aarch64-unknown-linux-gnu.tar.gz",
            "windows/amd64": "agent-desktop-v{v}-x86_64-pc-windows-msvc.tar.gz",
            "windows/arm64": "agent-desktop-v{v}-aarch64-pc-windows-msvc.tar.gz",
        },
    },
}

VERSION = re.compile(r"^v?(\d+)\.(\d+)\.(\d+)$")


def get(url: str, accept: str = "application/vnd.github+json") -> bytes:
    req = urllib.request.Request(url, headers={"Accept": accept, "User-Agent": "tamesu-pin-updater"})
    token = os.environ.get("GITHUB_TOKEN") or os.environ.get("GH_TOKEN")
    if token and url.startswith(API):
        req.add_header("Authorization", f"Bearer {token}")
    with urllib.request.urlopen(req, timeout=120) as res:
        return res.read()


def parse_version(tag: str) -> tuple[int, int, int] | None:
    m = VERSION.match(tag)
    return (int(m[1]), int(m[2]), int(m[3])) if m else None


def newest_release(repo: str, min_age: dt.timedelta, now: dt.datetime) -> dict | None:
    """The newest stable release published at least min_age ago."""
    releases = json.loads(get(f"{API}/repos/{repo}/releases?per_page=100"))
    best = None
    for r in releases:
        version = parse_version(r.get("tag_name", ""))
        if version is None or r.get("draft") or r.get("prerelease") or not r.get("published_at"):
            continue
        published = dt.datetime.fromisoformat(r["published_at"].replace("Z", "+00:00"))
        if now - published < min_age:
            continue
        if best is None or version > parse_version(best["tag_name"]):
            best = r
    return best


def digest(asset: dict) -> str:
    """The SHA-256 of a release file: GitHub's own digest, or the hash of a download."""
    d = asset.get("digest") or ""
    if d.startswith("sha256:") and len(d) == len("sha256:") + 64:
        return d[len("sha256:") :]
    return hashlib.sha256(get(asset["browser_download_url"], accept="application/octet-stream")).hexdigest()


def published_checksums(release: dict) -> dict[str, str]:
    """The release's checksums.txt, when it ships one: file name to SHA-256."""
    for a in release.get("assets", []):
        if a["name"] == "checksums.txt":
            out = {}
            for line in get(a["browser_download_url"], accept="application/octet-stream").decode().splitlines():
                parts = line.split()
                if len(parts) == 2:
                    out[parts[1].lstrip("*")] = parts[0].lower()
            return out
    return {}


def pin_for(name: str, release: dict) -> dict:
    spec = TOOLS[name]
    version = release["tag_name"].lstrip("v")
    by_name = {a["name"]: a for a in release.get("assets", [])}
    sums = published_checksums(release)
    assets = {}
    for platform, template in sorted(spec["assets"].items()):
        file = template.format(v=version)
        if file not in by_name:
            raise SystemExit(f"{name} {version}: the release has no {file} ({platform})")
        sha = digest(by_name[file]).lower()
        if file in sums and sums[file] != sha:
            raise SystemExit(f"{name} {version}: {file} is {sha} but checksums.txt says {sums[file]}")
        assets[platform] = {"name": file, "sha256": sha}
    return {"repo": spec["repo"], "version": version, "assets": assets}


def output(**values: str) -> None:
    path = os.environ.get("GITHUB_OUTPUT")
    if not path:
        return
    with open(path, "a", encoding="utf-8") as f:
        for k, v in values.items():
            f.write(f"{k}={v}\n")


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--file", default="internal/pinned/pins.json", help="the pin file to rewrite")
    ap.add_argument("--tool", default="all", choices=["all", *TOOLS], help="which tool to update")
    ap.add_argument("--min-age-days", type=float, default=5, help="how long a release must have been public")
    args = ap.parse_args()

    path = Path(args.file)
    pins = json.loads(path.read_text(encoding="utf-8")) if path.exists() else {}
    now = dt.datetime.now(dt.timezone.utc)
    changed, old, new = False, "", ""
    for name in TOOLS if args.tool == "all" else [args.tool]:
        current = pins.get(name, {}).get("version", "")
        release = newest_release(TOOLS[name]["repo"], dt.timedelta(days=args.min_age_days), now)
        if release is None:
            print(f"{name}: no release is at least {args.min_age_days:g} days old; pin stays at {current or 'none'}")
            continue
        latest = release["tag_name"].lstrip("v")
        if current and parse_version(latest) <= parse_version(current):
            print(f"{name}: {current} is current")
            continue
        pins[name] = pin_for(name, release)
        changed, old, new = True, current, latest
        print(f"{name}: {current or 'none'} -> {latest}")
    if changed:
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(pins, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    output(changed=str(changed).lower(), old=old, new=new)
    return 0


if __name__ == "__main__":
    sys.exit(main())
