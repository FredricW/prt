import { useEffect, useState } from "react";

import { getUser, type User } from "../api/client";
import { listUsers } from "../api/users";
import { UserCard } from "./UserCard";

interface Props {
  pinnedIds?: string[];
}

export function UserList({ pinnedIds = [] }: Props) {
  const [users, setUsers] = useState<User[]>([]);
  const [pinned, setPinned] = useState<User[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);

  useEffect(() => {
    listUsers().then((page) => {
      setUsers(page.users);
      setCursor(page.nextCursor);
    });
  }, []);

  useEffect(() => {
    Promise.all(pinnedIds.map((id) => getUser(id))).then(setPinned);
  }, [pinnedIds]);

  const loadMore = async () => {
    if (!cursor) return;
    const page = await listUsers(cursor);
    setUsers((prev) => [...prev, ...page.users]);
    setCursor(page.nextCursor);
  };

  return (
    <section>
      {pinned.length > 0 && (
        <ul className="pinned">
          {pinned.map((u) => (
            <li key={u.id}>
              <UserCard userId={u.id} compact />
            </li>
          ))}
        </ul>
      )}
      <ul>
        {users.map((u) => (
          <li key={u.id}>
            <UserCard userId={u.id} />
          </li>
        ))}
      </ul>
      {cursor && <button onClick={loadMore}>Load more</button>}
    </section>
  );
}
