import { useState } from "react";

interface Props {
  name: string;
}

export function Card({ name }: Props) {
  const [open, setOpen] = useState(false);
  return <div onClick={() => setOpen(!open)}>{name}</div>;
}

export const Badge = ({ label }: { label: string }) => <span className="badge">{label}</span>;
