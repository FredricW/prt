import { fetchJSON, type User } from "./client";

export interface Team {
  id: string;
  name: string;
  memberCount: number;
}

export async function listTeams(): Promise<Team[]> {
  return fetchJSON<Team[]>("/teams");
}

export async function teamMembers(teamId: string): Promise<User[]> {
  return fetchJSON<User[]>(`/teams/${encodeURIComponent(teamId)}/members`);
}

export async function addMember(teamId: string, userId: string): Promise<void> {
  await fetchJSON<void>(`/teams/${encodeURIComponent(teamId)}/members`, {
    method: "POST",
    body: { userId },
  });
}
