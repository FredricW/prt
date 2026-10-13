import { useState } from "react";

import { updateUser } from "../api/client";
import { useUser } from "../hooks/useUser";
import { formatDate } from "../utils/format";

interface Props {
  userId: string;
}

export function ProfilePage({ userId }: Props) {
  const { user, error } = useUser(userId);
  const [name, setName] = useState("");
  const [saving, setSaving] = useState(false);

  if (error) {
    return <p role="alert">Could not load profile: {error.message}</p>;
  }
  if (!user) {
    return <p>Loading…</p>;
  }

  const save = async () => {
    setSaving(true);
    try {
      await updateUser(user.id, { displayName: name || user.displayName });
    } finally {
      setSaving(false);
    }
  };

  return (
    <main className="profile">
      <h1>{user.displayName}</h1>
      <p className="muted">
        {user.email} · member since {formatDate(user.createdAt)}
      </p>
      <label>
        Display name
        <input value={name} placeholder={user.displayName} onChange={(e) => setName(e.target.value)} />
      </label>
      <button disabled={saving} onClick={save}>
        {saving ? "Saving…" : "Save"}
      </button>
    </main>
  );
}
