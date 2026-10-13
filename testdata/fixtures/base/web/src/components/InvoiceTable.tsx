import { useEffect, useState } from "react";

import { fetchJSON } from "../api/client";
import { type Invoice } from "../api/billing";
import { formatDate, formatMoney } from "../utils/format";

export function InvoiceTable() {
  const [invoices, setInvoices] = useState<Invoice[]>([]);

  useEffect(() => {
    fetchJSON<Invoice[]>("/billing/invoices").then(setInvoices);
  }, []);

  return (
    <table className="invoices">
      <thead>
        <tr>
          <th>Number</th>
          <th>Issued</th>
          <th>Status</th>
          <th className="num">Amount</th>
        </tr>
      </thead>
      <tbody>
        {invoices.map((inv) => (
          <tr key={inv.id}>
            <td>{inv.number}</td>
            <td>{formatDate(inv.issuedAt)}</td>
            <td>{inv.status}</td>
            <td className="num">{formatMoney(inv.amountCents, inv.currency)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
