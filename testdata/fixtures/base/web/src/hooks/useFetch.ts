import { useEffect, useState } from "react";

import { fetchJSON, type RequestOptions } from "../api/client";

export interface FetchState<T> {
  data: T | null;
  error: Error | null;
  loading: boolean;
}

// useFetch loads `path` with fetchJSON and aborts the request on unmount or
// when the path changes.
export function useFetch<T>(path: string | null, opts: Omit<RequestOptions, "signal"> = {}): FetchState<T> {
  const [state, setState] = useState<FetchState<T>>({ data: null, error: null, loading: path !== null });
  const key = JSON.stringify(opts);

  useEffect(() => {
    if (path === null) {
      setState({ data: null, error: null, loading: false });
      return;
    }
    const ctrl = new AbortController();
    setState((s) => ({ ...s, loading: true }));
    fetchJSON<T>(path, { ...opts, signal: ctrl.signal })
      .then((data) => setState({ data, error: null, loading: false }))
      .catch((error: Error) => {
        if (error.name !== "AbortError") {
          setState({ data: null, error, loading: false });
        }
      });
    return () => ctrl.abort();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [path, key]);

  return state;
}
