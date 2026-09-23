#!/usr/bin/env python3
"""Generate the data used to build the wiki's "CI-Status" page.

Discovers every presubmit `openshift-e2e` job defined for the medik8s
operators in openshift/release (ci-operator/config/medik8s/<operator>/),
then queries GCS directly for each job's most recent completed run to get
its real current status (Prow's own `/badge.svg` only reflects live
ProwJob objects, which are garbage-collected shortly after a presubmit
finishes, so it is not usable as a durable "latest status" signal).

Writes two JSON files into --out-dir:
    data.json      - operator -> branch -> OCP version job matrix
    statuses.json  - latest known result per job (state, timestamp, build)

These are the inputs consumed by render_markdown.py to produce the
"CI-Status" wiki page.

Requires only the Python standard library. Uses the public GitHub API
(set GITHUB_TOKEN to raise the unauthenticated rate limit if needed) and
the public GCS HTTP endpoints for the `test-platform-results` bucket.

Usage:
    python3 scripts/ci-status/generate_data.py [--out-dir DIR] [--workers N]
"""

import argparse
import json
import os
import re
import sys
import urllib.error
import urllib.request
from collections import Counter, defaultdict
from concurrent.futures import ThreadPoolExecutor, as_completed

OPERATORS = [
    ("fence-agents-remediation", "FAR", "Fence Agents Remediation"),
    ("machine-deletion-remediation", "MDR", "Machine Deletion Remediation"),
    ("node-healthcheck-operator", "NHC", "Node Health Check"),
    ("node-maintenance-operator", "NMO", "Node Maintenance Operator"),
    ("self-node-remediation", "SNR", "Self Node Remediation"),
    ("storage-based-remediation", "SBR", "Storage-Based Remediation"),
    ("customized-user-remediation", "CUR", "Customized User Remediation"),
]

GITHUB_API = "https://api.github.com"
GCS_BUCKET = "https://storage.googleapis.com/test-platform-results"

FNAME_RE = re.compile(
    r"^medik8s-(?P<repo>.+)-(?P<branch>main|release-[0-9]+\.[0-9]+)__(?P<variant>[0-9]+\.[0-9]+)\.yaml$"
)


def github_headers():
    headers = {"Accept": "application/vnd.github+json", "User-Agent": "medik8s-ci-status-script"}
    token = os.environ.get("GITHUB_TOKEN")
    if token:
        headers["Authorization"] = f"Bearer {token}"
    return headers


def list_config_files(operator_dir):
    url = f"{GITHUB_API}/repos/openshift/release/contents/ci-operator/config/medik8s/{operator_dir}"
    req = urllib.request.Request(url, headers=github_headers())
    with urllib.request.urlopen(req, timeout=30) as resp:
        entries = json.load(resp)
    return [e["name"] for e in entries if e.get("type") == "file"]


def version_sort_key(v):
    return tuple(float(x) for x in v.split("."))


def discover_jobs():
    """Return {operator_dir: {"abbrev", "fullname", "repo", "branches": {branch: [versions]}}}."""
    data = {}
    for dirname, abbrev, fullname in OPERATORS:
        files = list_config_files(dirname)
        rows = defaultdict(list)
        for f in files:
            if not f.endswith(".yaml"):
                continue
            m = FNAME_RE.match(f)
            if not m:
                print(f"WARN: no match for {dirname}/{f}", file=sys.stderr)
                continue
            rows[m.group("branch")].append(m.group("variant"))
        for branch in rows:
            rows[branch] = sorted(set(rows[branch]), key=version_sort_key)
        data[dirname] = {
            "abbrev": abbrev,
            "fullname": fullname,
            "repo": dirname,
            "branches": dict(rows),
        }
    return data


def iter_jobs(data):
    for repo, d in data.items():
        for branch, versions in d["branches"].items():
            for v in versions:
                yield f"pull-ci-medik8s-{repo}-{branch}-{v}-openshift-e2e"


def _get_text(url):
    try:
        with urllib.request.urlopen(url, timeout=15) as resp:
            return resp.read().decode()
    except urllib.error.HTTPError:
        return None


def _get_json(url):
    text = _get_text(url)
    return json.loads(text) if text is not None else None


def _head(url):
    req = urllib.request.Request(url, method="HEAD")
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            return dict(resp.headers)
    except urllib.error.HTTPError:
        return None


def fetch_status(job):
    """Look up the real latest result for a presubmit job directly from GCS.

    latest-build.txt -> build's metadata (x-goog-meta-link header) -> finished.json
    """
    try:
        build = _get_text(f"{GCS_BUCKET}/pr-logs/directory/{job}/latest-build.txt")
        if build is None:
            return {"state": "no-history"}
        build = build.strip()

        headers = _head(f"{GCS_BUCKET}/pr-logs/directory/{job}/{build}.txt")
        if headers is None:
            return {"state": "no-history"}
        link = next(
            (v for k, v in headers.items() if k.lower() == "x-goog-meta-x-goog-meta-link"),
            None,
        )
        if not link:
            return {"state": "unknown", "build": build}

        gs_path = link.replace("gs://test-platform-results/", "")
        finished = _get_json(f"{GCS_BUCKET}/{gs_path}/finished.json")
        if finished is None:
            return {"state": "pending", "build": build}

        return {
            "state": finished.get("result", "UNKNOWN"),
            "passed": finished.get("passed"),
            "timestamp": finished.get("timestamp"),
            "build": build,
        }
    except Exception as e:
        return {"state": "error", "error": str(e)}


def fetch_all_statuses(jobs, workers):
    results = {}
    with ThreadPoolExecutor(max_workers=workers) as ex:
        futs = {ex.submit(fetch_status, j): j for j in jobs}
        for fut in as_completed(futs):
            results[futs[fut]] = fut.result()
    return results


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument(
        "--out-dir",
        default=os.path.dirname(os.path.abspath(__file__)),
        help="directory to write data.json and statuses.json into (default: this script's directory)",
    )
    parser.add_argument("--workers", type=int, default=16, help="concurrent GCS status lookups (default: 16)")
    args = parser.parse_args()

    os.makedirs(args.out_dir, exist_ok=True)

    print("Discovering jobs from openshift/release ci-operator/config/medik8s/...")
    data = discover_jobs()
    jobs = list(iter_jobs(data))
    print(f"Found {len(jobs)} presubmit openshift-e2e jobs across {len(data)} operators.")

    print("Fetching latest status for each job from GCS...")
    statuses = fetch_all_statuses(jobs, args.workers)

    data_path = os.path.join(args.out_dir, "data.json")
    statuses_path = os.path.join(args.out_dir, "statuses.json")
    with open(data_path, "w") as fh:
        json.dump(data, fh, indent=2)
    with open(statuses_path, "w") as fh:
        json.dump(statuses, fh, indent=2)

    counts = Counter(v["state"] for v in statuses.values())
    print(f"Wrote {data_path}")
    print(f"Wrote {statuses_path}")
    print(f"Status summary: {dict(counts)}")


if __name__ == "__main__":
    main()
