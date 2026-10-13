import { useEffect, useState } from "react";

import { fetchJSON, type User } from "../api/client";
import { type Team } from "../api/teams";
import { UserCard } from "./UserCard";

interface Props {
  teamId: string;
}

export function TeamPanel({ teamId }: Props) {
  const [team, setTeam] = useState<Team | null>(null);
  const [members, setMembers] = useState<User[]>([]);

  useEffect(() => {
    fetchJSON<Team>(`/teams/${teamId}`).then(setTeam);
    fetchJSON<User[]>(`/teams/${teamId}/members`).then(setMembers);
  }, [teamId]);

  if (!team) return null;
  return (
    <aside className="team-panel">
      <h2>
        {team.name} <small>({team.memberCount})</small>
      </h2>
      {members.map((m) => (
        <UserCard key={m.id} userId={m.id} compact />
      ))}
    </aside>
  );
}
