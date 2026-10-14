package store

import (
	"context"

	"github.com/google/uuid"
)

// TrialBalanceRow aggregates one currency of a ledger. Sums are returned as
// decimal strings because they are NUMERIC and may exceed int64.
type TrialBalanceRow struct {
	Currency       string `json:"currency"`
	Accounts       int64  `json:"accounts"`
	TotalDebits    string `json:"total_debits"`
	TotalCredits   string `json:"total_credits"`
	DebitBalances  string `json:"debit_balances"`
	CreditBalances string `json:"credit_balances"`
	Balanced       bool   `json:"balanced"`
}

// TrialBalance sums posted activity per currency. In a correct ledger total
// debits equal total credits, and so do the sums of debit- and credit-side
// account balances.
func TrialBalance(ctx context.Context, db DBTX, ledgerID uuid.UUID) ([]TrialBalanceRow, error) {
	rows, err := db.Query(ctx, `
		SELECT currency, count(*),
		       sum(posted_debits)::text, sum(posted_credits)::text,
		       sum(GREATEST(posted_debits - posted_credits, 0))::text,
		       sum(GREATEST(posted_credits - posted_debits, 0))::text,
		       sum(posted_debits) = sum(posted_credits)
		  FROM accounts
		 WHERE ledger_id = $1
		 GROUP BY currency
		 ORDER BY currency`, ledgerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TrialBalanceRow{}
	for rows.Next() {
		var r TrialBalanceRow
		if err := rows.Scan(&r.Currency, &r.Accounts, &r.TotalDebits, &r.TotalCredits,
			&r.DebitBalances, &r.CreditBalances, &r.Balanced); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
