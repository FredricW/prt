import { fetchJSON } from "./client";

export interface Org {
  id: string;
  name: string;
  plan: "free" | "team" | "enterprise";
  seats: number;
}

export async function currentOrg(): Promise<Org> {
  return fetchJSON<Org>("/orgs/current");
}

export async function renameOrg(name: string): Promise<Org> {
  return fetchJSON<Org>("/orgs/current", { method: "PATCH", body: { name } });
}
