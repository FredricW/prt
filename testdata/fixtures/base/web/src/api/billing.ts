import { fetchJSON } from "./client";

export interface Invoice {
  id: string;
  number: string;
  amountCents: number;
  currency: string;
  status: "draft" | "open" | "paid" | "void";
  issuedAt: string;
}

export async function listInvoices(status?: Invoice["status"]): Promise<Invoice[]> {
  return fetchJSON<Invoice[]>("/billing/invoices", { query: { status } });
}

export async function downloadInvoiceUrl(id: string): Promise<string> {
  const { url } = await fetchJSON<{ url: string }>(`/billing/invoices/${encodeURIComponent(id)}/pdf`);
  return url;
}
