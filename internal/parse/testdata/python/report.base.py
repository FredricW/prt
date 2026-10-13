import os


class Report:
    def __init__(self, rows):
        self.rows = rows

    def render(self):
        return "\n".join(str(r) for r in self.rows)


async def fetch(client, url):
    resp = await client.get(url)
    return resp.json()
