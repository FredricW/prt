import { fetchJSON } from "./client";

export interface Settings {
  locale: string;
  timezone: string;
  emailDigest: "never" | "daily" | "weekly";
}

export async function getSettings(): Promise<Settings> {
  return fetchJSON<Settings>("/settings");
}

export async function saveSettings(s: Settings): Promise<Settings> {
  return fetchJSON<Settings>("/settings", { method: "PUT", body: s });
}
