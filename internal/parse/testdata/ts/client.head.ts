import { formatQuery } from "./format";
import { trace } from "./trace";

export interface User {
  id: string;
  name: string;
}

export class ApiClient {
  constructor(private readonly base: string) {}

  async getUser(id: string, opts: { fresh?: boolean } = {}): Promise<User> {
    const res = await fetch(`${this.base}/users/${id}`, { cache: opts.fresh ? "no-cache" : "default" });
    return (await res.json()) as User;
  }

  async listUsers(): Promise<User[]> {
    const res = await fetch(`${this.base}/users`);
    return (await res.json()) as User[];
  }
}

export const toQuery = (q: Record<string, string>): string => {
  return trace(formatQuery(q));
};

export function plain(a: number): number {
  return a * 3;
}
