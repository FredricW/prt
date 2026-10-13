import { afterEach, describe, expect, it, vi } from "vitest";

import * as client from "../api/client";
import { deactivateUser, getUsers, listUsers } from "../api/users";

afterEach(() => {
  vi.restoreAllMocks();
});

describe("users api", () => {
  it("passes cursor and limit to fetchJSON", async () => {
    const spy = vi.spyOn(client, "fetchJSON").mockResolvedValue({ users: [], nextCursor: null });
    await listUsers("c1", 10);
    expect(spy).toHaveBeenCalledWith("/users", { query: { cursor: "c1", limit: 10 } });
  });

  it("loads users one by one", async () => {
    vi.spyOn(client, "getUser").mockImplementation(async (id) => ({ id }) as client.User);
    const users = await getUsers(["u1", "u2"]);
    expect(users.map((u) => u.id)).toEqual(["u1", "u2"]);
  });

  it("deactivates with POST", async () => {
    const spy = vi.spyOn(client, "fetchJSON").mockResolvedValue(undefined);
    await deactivateUser("u1");
    expect(spy).toHaveBeenCalledWith("/users/u1/deactivate", { method: "POST" });
  });
});
