import { useEffect, useState } from "react";

import { fetchJSON } from "../api/client";
import { type Health } from "../api/health";

export function StatusFooter() {
  const [health, setHealth] = useState<Health | null>(null);

  useEffect(() => {
    fetchJSON<Health>("/health")
      .then(setHealth)
      .catch(() => setHealth({ status: "degraded", version: "?" }));
  }, []);

  return (
    <footer className={`status status--${health?.status ?? "unknown"}`}>
      API {health?.status ?? "…"} · v{health?.version ?? "?"}
    </footer>
  );
}
