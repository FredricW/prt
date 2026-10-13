import { fetchJSON } from "./client";

export interface Notification {
  id: string;
  title: string;
  read: boolean;
  createdAt: string;
}

export async function listNotifications(unreadOnly = false): Promise<Notification[]> {
  return fetchJSON<Notification[]>("/notifications", { query: { unread: unreadOnly || undefined } });
}

export async function markRead(id: string): Promise<void> {
  await fetchJSON<void>(`/notifications/${encodeURIComponent(id)}/read`, { method: "POST" });
}
