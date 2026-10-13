import { useEffect, useState } from "react";

import { getUser, type User } from "../api/client";

export function useUser(id: string | undefined): { user: User | null; error: Error | null } {
  const [user, setUser] = useState<User | null>(null);
  const [error, setError] = useState<Error | null>(null);

  useEffect(() => {
    if (!id) {
      setUser(null);
      return;
    }
    let live = true;
    getUser(id)
      .then((u) => live && setUser(u))
      .catch((e: Error) => live && setError(e));
    return () => {
      live = false;
    };
  }, [id]);

  return { user, error };
}
