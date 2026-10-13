import { useEffect, useState } from "react";

import { search, type SearchHit } from "../api/search";

// useSearch debounces `q` and cancels the in-flight request when it changes.
export function useSearch(q: string, delayMs = 200): SearchHit[] {
  const [hits, setHits] = useState<SearchHit[]>([]);

  useEffect(() => {
    const ctrl = new AbortController();
    const t = window.setTimeout(() => {
      search(q, ctrl.signal)
        .then(setHits)
        .catch(() => setHits([]));
    }, delayMs);
    return () => {
      window.clearTimeout(t);
      ctrl.abort();
    };
  }, [q, delayMs]);

  return hits;
}
