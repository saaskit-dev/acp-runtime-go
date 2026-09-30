#!/usr/bin/env python3
"""Observe official stable schema releases, without changing the working pin."""
import datetime
import difflib
import hashlib
import json
import pathlib
import re
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parents[2]
OUTPUT = ROOT / "schema-observation"
OUTPUT.mkdir(exist_ok=True)
LOCK_ROOT = ROOT / "testdata/acp"
lock = json.loads((LOCK_ROOT / "schema-lock.json").read_text())
pinned = next(release for release in lock["releases"] if release["tag"] == lock["target"])
metadata = {"observedAt": datetime.datetime.now(datetime.timezone.utc).isoformat(), "pinned": pinned, "status": "INCOMPLETE"}

def fetch(url):
    request = urllib.request.Request(url, headers={"Accept": "application/vnd.github+json", "User-Agent": "acp-stable-schema-check"})
    with urllib.request.urlopen(request, timeout=30) as response:
        return response.read()

try:
    releases = json.loads(fetch("https://api.github.com/repos/agentclientprotocol/agent-client-protocol/releases?per_page=100"))
    tags = json.loads(fetch("https://api.github.com/repos/agentclientprotocol/agent-client-protocol/tags?per_page=100"))
    tag_index = {tag["name"]: tag for tag in tags}
    candidates = []
    for release in releases:
        name = release.get("tag_name", "")
        match = re.fullmatch(r"schema-v(\d+)\.(\d+)\.(\d+)", name)
        # A stable-looking tag is insufficient: require an actual published,
        # explicitly non-prerelease release before treating it as stable.
        if match and release.get("prerelease") is False and release.get("draft") is False:
            candidates.append((tuple(map(int, match.groups())), name))
    if not candidates:
        raise RuntimeError("No published stable schema release in official release response")
    latest_name = max(candidates, key=lambda item: item[0])[1]
    if latest_name not in tag_index:
        raise RuntimeError("Latest stable release tag was not resolved to an immutable commit")
    latest = tag_index[latest_name]
    commit = latest["commit"]["sha"]
    if not re.fullmatch(r"[0-9a-f]{40}", commit):
        raise RuntimeError("Official tag response has invalid commit identity")
    source = f"https://raw.githubusercontent.com/agentclientprotocol/agent-client-protocol/{commit}/schema/v1/schema.json"
    raw = fetch(source)
    current = json.loads(raw)
    previous_raw = (LOCK_ROOT / pinned["path"]).read_bytes()
    previous = json.loads(previous_raw)
    if hashlib.sha256(previous_raw).hexdigest() != pinned["sha256"]:
        raise RuntimeError("Checked-in pinned schema digest does not match lock")
    digest = hashlib.sha256(raw).hexdigest()
    metadata.update({"status": "UNCHANGED" if digest == pinned["sha256"] else "REVIEW_NEEDED", "latest": {"tag": latest["name"], "commit": commit, "sha256": digest, "source": source}, "stablePathOnly": "schema/v1/schema.json"})
    (OUTPUT / "latest-schema.json").write_bytes(raw)
    diff = difflib.unified_diff(json.dumps(previous, indent=2, sort_keys=True).splitlines(True), json.dumps(current, indent=2, sort_keys=True).splitlines(True), fromfile=pinned["tag"], tofile=latest["name"])
    (OUTPUT / "stable-schema.diff").write_text("".join(diff))
    print(f"{metadata['status']}: pinned {pinned['tag']}; latest stable {latest['name']}; SHA256 {digest}")
except Exception as error:
    metadata["reason"] = str(error)
    raise
finally:
    (OUTPUT / "metadata.json").write_text(json.dumps(metadata, indent=2) + "\n")
