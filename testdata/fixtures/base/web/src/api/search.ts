import { fetchJSON } from "./client";

export interface SearchHit {
  kind: "user" | "team" | "invoice";
  id: string;
  label: string;
}

export async function search(q: string, signal?: AbortSignal): Promise<SearchHit[]> {
  if (q.trim() === "") {
    return [];
  }
  return fetchJSON<SearchHit[]>("/search", { query: { q }, signal });
}
