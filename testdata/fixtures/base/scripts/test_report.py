import unittest

from report import monthly_totals, render


class MonthlyTotalsTest(unittest.TestCase):
    def test_only_paid_invoices_count(self) -> None:
        invoices = [
            {"status": "paid", "issuedAt": "2026-01-03T10:00:00Z", "amountCents": 1000},
            {"status": "open", "issuedAt": "2026-01-04T10:00:00Z", "amountCents": 5000},
            {"status": "paid", "issuedAt": "2026-02-01T00:00:00Z", "amountCents": 250},
        ]
        self.assertEqual(monthly_totals(invoices), {"2026-01": 1000, "2026-02": 250})

    def test_render(self) -> None:
        self.assertIn("2026-01       10.00", render({"2026-01": 1000}))


if __name__ == "__main__":
    unittest.main()
