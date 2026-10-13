import { useEffect, useState } from "react";

import { fetchJSON } from "../api/client";
import { formatDate } from "../utils/format";

interface AuditEntry {
  id: string;
  actor: string;
  action: string;
  at: string;
}

export function AuditLog({ limit = 25 }: { limit?: number }) {
  const [entries, setEntries] = useState<AuditEntry[]>([]);

  useEffect(() => {
    fetchJSON<AuditEntry[]>("/audit", { query: { limit } }).then(setEntries);
  }, [limit]);

  return (
    <ol className="audit">
      {entries.map((e) => (
        <li key={e.id}>
          <time>{formatDate(e.at)}</time> <b>{e.actor}</b> {e.action}
        </li>
      ))}
    </ol>
  );
}
