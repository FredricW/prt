import { fetchJSON } from "./client";

export interface Health {
  status: "ok" | "degraded";
  version: string;
}

// ping is used by the status footer; it never throws.
export async function ping(): Promise<Health | null> {
  try {
    return await fetchJSON<Health>("/health");
  } catch {
    return null;
  }
}
