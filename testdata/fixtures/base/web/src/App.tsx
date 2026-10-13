import { useState } from "react";

import { InvoiceTable } from "./components/InvoiceTable";
import { NotificationBell } from "./components/NotificationBell";
import { ProfilePage } from "./components/ProfilePage";
import { SearchBox } from "./components/SearchBox";
import { SettingsForm } from "./components/SettingsForm";
import { StatusFooter } from "./components/StatusFooter";
import { UserList } from "./components/UserList";
import "./styles/theme.css";

type Route = { page: "users" } | { page: "profile"; id: string } | { page: "billing" } | { page: "settings" };

export function App() {
  const [route, setRoute] = useState<Route>({ page: "users" });

  return (
    <div className="app">
      <header>
        <nav>
          <button onClick={() => setRoute({ page: "users" })}>Users</button>
          <button onClick={() => setRoute({ page: "billing" })}>Billing</button>
          <button onClick={() => setRoute({ page: "settings" })}>Settings</button>
        </nav>
        <SearchBox onPick={(kind, id) => kind === "user" && setRoute({ page: "profile", id })} />
        <NotificationBell />
      </header>
      {route.page === "users" && <UserList />}
      {route.page === "profile" && <ProfilePage userId={route.id} />}
      {route.page === "billing" && <InvoiceTable />}
      {route.page === "settings" && <SettingsForm />}
      <StatusFooter />
    </div>
  );
}
