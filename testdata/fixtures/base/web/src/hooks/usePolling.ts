import { useEffect, useRef, useState } from "react";

import { fetchJSON } from "../api/client";

// usePolling re-fetches `path` every `intervalMs` while the tab is visible.
export function usePolling<T>(path: string, intervalMs = 30_000): T | null {
  const [data, setData] = useState<T | null>(null);
  const timer = useRef<number | undefined>(undefined);

  useEffect(() => {
    let cancelled = false;
    const tick = async () => {
      if (document.visibilityState === "visible") {
        try {
          const next = await fetchJSON<T>(path);
          if (!cancelled) {
            setData(next);
          }
        } catch (err) {
          console.warn("poll failed", path, err);
        }
      }
      timer.current = window.setTimeout(tick, intervalMs);
    };
    void tick();
    return () => {
      cancelled = true;
      window.clearTimeout(timer.current);
    };
  }, [path, intervalMs]);

  return data;
}
