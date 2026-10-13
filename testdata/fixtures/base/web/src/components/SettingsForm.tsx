import { useEffect, useState } from "react";

import { fetchJSON } from "../api/client";
import { type Settings } from "../api/settings";

export function SettingsForm() {
  const [settings, setSettings] = useState<Settings | null>(null);
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    fetchJSON<Settings>("/settings").then(setSettings);
  }, []);

  if (!settings) return null;

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const next = await fetchJSON<Settings>("/settings", { method: "PUT", body: settings });
    setSettings(next);
    setSaved(true);
  };

  return (
    <form onSubmit={submit}>
      <label>
        Email digest
        <select
          value={settings.emailDigest}
          onChange={(e) => setSettings({ ...settings, emailDigest: e.target.value as Settings["emailDigest"] })}
        >
          <option value="never">Never</option>
          <option value="daily">Daily</option>
          <option value="weekly">Weekly</option>
        </select>
      </label>
      <button type="submit">Save</button>
      {saved && <span role="status">Saved</span>}
    </form>
  );
}
