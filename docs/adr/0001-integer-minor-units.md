# ADR 0001: Store money as integer minor units, send it as strings

**Status:** accepted

## Context
Floating point cannot represent most decimal fractions exactly
(`0.1 + 0.2 != 0.3`). Rounding errors in a ledger accumulate silently and
break the debits = credits invariant.

## Decision
* Store every amount as `BIGINT` minor units (cents for USD, satoshis for BTC).
  A `currencies` table records each currency's exponent.
* In Go, use `money.Amount` (`int64`) with overflow-checked `Add`/`Sub`.
  Database aggregates use `NUMERIC`, so sums cannot overflow either.
* On the wire, encode amounts as JSON **strings** of digits (`"1050"`), and
  reject JSON numbers and decimals.

## Consequences
* There is no rounding anywhere in the core. Currency conversion and rounding
  policy belong to the caller, who posts the already-converted minor-unit amounts.
* JavaScript clients cannot lose precision above 2^53.
* The maximum is about 9.2 × 10^18 minor units per counter, far beyond realistic
  balances. Overflow is detected and rejected (`amount_overflow`).
