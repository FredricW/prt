import { formatQuery } from "./format";

export interface User {
  id: string;
  name: string;
}

export class ApiClient {
  constructor(private readonly base: string) {}

  async getUser(id: string): Promise<User> {
    const res = await fetch(`${this.base}/users/${id}`);
    return (await res.json()) as User;
  }

  async listUsers(): Promise<User[]> {
    const res = await fetch(`${this.base}/users`);
    return (await res.json()) as User[];
  }
}

export const toQuery = (q: Record<string, string>): string => {
  return formatQuery(q);
};

export function plain(a: number): number {
  return a * 2;
}
