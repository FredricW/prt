import { afterEach, describe, expect, it, vi } from "vitest";

import { ApiError, fetchJSON, getUser } from "../api/client";

function mockFetch(status: number, body: unknown) {
  const res = new Response(body === undefined ? null : JSON.stringify(body), { status });
  return vi.spyOn(globalThis, "fetch").mockResolvedValue(res);
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("fetchJSON", () => {
  it("prefixes the API base and encodes the query", async () => {
    const spy = mockFetch(200, { ok: true });
    await fetchJSON("/things", { query: { b: 2, a: "x y" } });
    expect(spy.mock.calls[0]![0]).toBe("/api/v1/things?a=x%20y&b=2");
  });

  it("maps error payloads to ApiError", async () => {
    mockFetch(404, { code: "not_found", message: "no such user" });
    await expect(fetchJSON("/users/nope")).rejects.toEqual(new ApiError(404, "not_found", "no such user"));
  });

  it("returns undefined for 204", async () => {
    mockFetch(204, undefined);
    await expect(fetchJSON("/noop", { method: "POST" })).resolves.toBeUndefined();
  });
});

describe("getUser", () => {
  it("escapes the id", async () => {
    const spy = mockFetch(200, { id: "a/b" });
    await getUser("a/b");
    expect(spy.mock.calls[0]![0]).toBe("/api/v1/users/a%2Fb");
  });
});
