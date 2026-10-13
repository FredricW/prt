import { useState } from "react";

import { useSearch } from "../hooks/useSearch";

export function SearchBox({ onPick }: { onPick: (kind: string, id: string) => void }) {
  const [q, setQ] = useState("");
  const hits = useSearch(q);

  return (
    <div className="search">
      <input type="search" value={q} placeholder="Search users, teams, invoices" onChange={(e) => setQ(e.target.value)} />
      {hits.length > 0 && (
        <ul role="listbox">
          {hits.map((h) => (
            <li key={`${h.kind}:${h.id}`} role="option" onClick={() => onPick(h.kind, h.id)}>
              <span className={`tag tag--${h.kind}`}>{h.kind}</span> {h.label}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
