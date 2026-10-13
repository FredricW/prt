import os
import sys


class Report:
    def __init__(self, rows):
        self.rows = rows

    def render(self):
        return "\n".join(repr(r) for r in self.rows)


async def fetch(client, url):
    resp = await client.get(url, timeout=5)
    return resp.json()
