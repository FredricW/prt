import { useEffect, useState } from "react";

import { getUser, type User } from "../api/client";
import { formatDate, initials } from "../utils/format";

interface Props {
  userId: string;
  compact?: boolean;
}

export function UserCard({ userId, compact = false }: Props) {
  const [user, setUser] = useState<User | null>(null);

  useEffect(() => {
    let live = true;
    getUser(userId).then((u) => {
      if (live) setUser(u);
    });
    return () => {
      live = false;
    };
  }, [userId]);

  if (!user) {
    return <div className="user-card user-card--loading" aria-busy="true" />;
  }
  return (
    <div className={compact ? "user-card user-card--compact" : "user-card"}>
      <span className="avatar">{initials(user.displayName)}</span>
      <div>
        <strong>{user.displayName}</strong>
        {!compact && <div className="muted">{user.email}</div>}
        {!compact && <div className="muted">Joined {formatDate(user.createdAt)}</div>}
      </div>
    </div>
  );
}
