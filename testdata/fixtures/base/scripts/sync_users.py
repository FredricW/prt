"""Sync users from the HR CSV export into the portal."""

from __future__ import annotations

import csv
import json
import os
import sys
import urllib.request

API = os.environ.get("PORTAL_API", "http://localhost:8080/api/v1")


def read_hr_export(path: str) -> list[dict[str, str]]:
    with open(path, newline="", encoding="utf-8") as f:
        return [row for row in csv.DictReader(f) if row.get("email")]


def upsert(user: dict[str, str], token: str) -> int:
    body = json.dumps({"email": user["email"], "displayName": user["name"], "teamId": user.get("team") or None})
    req = urllib.request.Request(
        f"{API}/users/{user['id']}",
        data=body.encode(),
        method="PUT",
        headers={"Authorization": f"Bearer {token}", "Content-Type": "application/json"},
    )
    with urllib.request.urlopen(req, timeout=10) as res:
        return res.status


def main(argv: list[str]) -> int:
    token = os.environ["PORTAL_TOKEN"]
    failed = 0
    for user in read_hr_export(argv[1]):
        try:
            upsert(user, token)
        except Exception as exc:  # noqa: BLE001 - report and continue
            failed += 1
            print(f"failed {user['email']}: {exc}", file=sys.stderr)
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
