import { useState } from "react";

import { fetchJSON } from "../api/client";
import { type Notification } from "../api/notifications";
import { usePolling } from "../hooks/usePolling";

export function NotificationBell() {
  const items = usePolling<Notification[]>("/notifications?unread=true") ?? [];
  const [open, setOpen] = useState(false);

  const dismiss = async (id: string) => {
    await fetchJSON<void>(`/notifications/${id}/read`, { method: "POST" });
  };

  return (
    <div className="bell">
      <button aria-label="Notifications" onClick={() => setOpen(!open)}>
        🔔 {items.length > 0 && <span className="badge">{items.length}</span>}
      </button>
      {open && (
        <ul className="bell__menu">
          {items.map((n) => (
            <li key={n.id}>
              {n.title} <button onClick={() => dismiss(n.id)}>Dismiss</button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
