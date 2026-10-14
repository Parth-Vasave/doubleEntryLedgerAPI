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

// Drift describes an account whose cached counters disagree with its journal.
type Drift struct {
	AccountID      uuid.UUID `json:"account_id"`
	LedgerID       uuid.UUID `json:"ledger_id"`
	PostedDebits   [2]int64  `json:"posted_debits"` // [cached, from entries]
	PostedCredits  [2]int64  `json:"posted_credits"`
	PendingDebits  [2]int64  `json:"pending_debits"`
	PendingCredits [2]int64  `json:"pending_credits"`
	Version        [2]int64  `json:"version"`
	MaxVersion     int64     `json:"max_entry_version"`
}

// Reconcile recomputes every account's counters from the entries table (the
// source of truth) and returns the accounts that disagree with their cached
// counters. Outstanding holds are pending entries whose transaction is still
// PENDING. The version must equal both the entry count and the highest entry
// version, i.e. versions are gapless. ledgerID nil checks all ledgers.
func Reconcile(ctx context.Context, db DBTX, ledgerID *uuid.UUID) ([]Drift, error) {
	rows, err := db.Query(ctx, `
		WITH j AS (
			SELECT e.account_id,
			       coalesce(sum(e.amount) FILTER (WHERE e.direction = 'DEBIT'  AND NOT e.pending), 0)::bigint AS pd,
			       coalesce(sum(e.amount) FILTER (WHERE e.direction = 'CREDIT' AND NOT e.pending), 0)::bigint AS pc,
			       coalesce(sum(e.amount) FILTER (WHERE e.direction = 'DEBIT'  AND e.pending AND t.status = 'PENDING'), 0)::bigint AS hd,
			       coalesce(sum(e.amount) FILTER (WHERE e.direction = 'CREDIT' AND e.pending AND t.status = 'PENDING'), 0)::bigint AS hc,
			       count(*) AS n,
			       max(e.account_version) AS maxv
			  FROM entries e
			  JOIN transactions t ON t.id = e.transaction_id
			 WHERE $1::uuid IS NULL OR e.ledger_id = $1
			 GROUP BY e.account_id
		)
		SELECT a.id, a.ledger_id,
		       a.posted_debits, coalesce(j.pd, 0), a.posted_credits, coalesce(j.pc, 0),
		       a.pending_debits, coalesce(j.hd, 0), a.pending_credits, coalesce(j.hc, 0),
		       a.version, coalesce(j.n, 0), coalesce(j.maxv, 0)
		  FROM accounts a
		  LEFT JOIN j ON j.account_id = a.id
		 WHERE ($1::uuid IS NULL OR a.ledger_id = $1)
		   AND (a.posted_debits   <> coalesce(j.pd, 0)
		     OR a.posted_credits  <> coalesce(j.pc, 0)
		     OR a.pending_debits  <> coalesce(j.hd, 0)
		     OR a.pending_credits <> coalesce(j.hc, 0)
		     OR a.version         <> coalesce(j.n, 0)
		     OR a.version         <> coalesce(j.maxv, 0))`, ledgerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Drift
	for rows.Next() {
		var d Drift
		if err := rows.Scan(&d.AccountID, &d.LedgerID,
			&d.PostedDebits[0], &d.PostedDebits[1], &d.PostedCredits[0], &d.PostedCredits[1],
			&d.PendingDebits[0], &d.PendingDebits[1], &d.PendingCredits[0], &d.PendingCredits[1],
			&d.Version[0], &d.Version[1], &d.MaxVersion); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
