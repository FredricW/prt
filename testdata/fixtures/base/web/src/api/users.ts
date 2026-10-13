import { fetchJSON, getUser, type User } from "./client";

export interface UserPage {
  users: User[];
  nextCursor: string | null;
}

export async function listUsers(cursor?: string, limit = 50): Promise<UserPage> {
  return fetchJSON<UserPage>("/users", { query: { cursor, limit } });
}

export async function getUsers(ids: string[]): Promise<User[]> {
  return Promise.all(ids.map((id) => getUser(id)));
}

export async function deactivateUser(id: string): Promise<void> {
  await fetchJSON<void>(`/users/${encodeURIComponent(id)}/deactivate`, { method: "POST" });
}
