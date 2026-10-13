"""Monthly revenue report from the portal billing API."""

from __future__ import annotations

import argparse
import collections
import datetime as dt
import json
import os
import urllib.request

API = os.environ.get("PORTAL_API", "http://localhost:8080/api/v1")


def fetch_invoices(org_id: str, token: str) -> list[dict]:
    req = urllib.request.Request(
        f"{API}/billing/invoices",
        headers={"Authorization": f"Bearer {token}", "X-Org-ID": org_id},
    )
    with urllib.request.urlopen(req, timeout=10) as res:
        return json.load(res)


def monthly_totals(invoices: list[dict]) -> dict[str, int]:
    totals: dict[str, int] = collections.defaultdict(int)
    for inv in invoices:
        if inv["status"] != "paid":
            continue
        month = dt.datetime.fromisoformat(inv["issuedAt"].replace("Z", "+00:00")).strftime("%Y-%m")
        totals[month] += inv["amountCents"]
    return dict(sorted(totals.items()))


def render(totals: dict[str, int]) -> str:
    lines = ["month    amount"]
    for month, cents in totals.items():
        lines.append(f"{month}  {cents / 100:>10.2f}")
    return "\n".join(lines)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("org")
    args = parser.parse_args()
    token = os.environ["PORTAL_TOKEN"]
    print(render(monthly_totals(fetch_invoices(args.org, token))))


if __name__ == "__main__":
    main()
