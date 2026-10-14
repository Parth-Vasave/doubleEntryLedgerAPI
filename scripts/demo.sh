#!/usr/bin/env bash
# End-to-end demo against a running stack (make up). Requires curl and jq.
#
# Walks through: ledger and chart of accounts, deposit, P2P payment with fee,
# idempotent retry, overdraft rejection, card-style hold with partial capture,
# refund via reversal, FX conversion, statement, historical balance, trial
# balance and webhooks.
set -euo pipefail

API=${API:-http://localhost:8080}
KEY=${ADMIN_API_KEY:-dev-admin-key-change-me}

bold() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
req() { # method path [json] [idempotency-key]
  local args=(-sS -X "$1" "$API$2" -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json')
  [[ -n "${3:-}" ]] && args+=(-d "$3")
  [[ -n "${4:-}" ]] && args+=(-H "Idempotency-Key: $4")
  curl "${args[@]}"
}
key() { uuidgen | tr 'A-Z' 'a-z'; }
acct() { # ledger code type currency allow_negative
  req POST "/v1/ledgers/$1/accounts" "{\"code\":\"$2\",\"name\":\"$2\",\"type\":\"$3\",\"currency\":\"$4\",\"allow_negative\":$5}" | jq -r .id
}
leg() { echo "{\"account_id\":\"$1\",\"direction\":\"$2\",\"amount\":\"$3\",\"currency\":\"$4\"}"; }
bal() { req GET "/v1/accounts/$1" | jq -c '{code, posted: .balances.posted, available: .balances.available, pending: .balances.pending}'; }

bold "Create ledger"
LEDGER=$(req POST /v1/ledgers '{"name":"demo"}' | jq -r .id)
echo "ledger $LEDGER"

bold "Chart of accounts"
BANK=$(acct "$LEDGER" assets:bank:usd ASSET USD true)
ALICE=$(acct "$LEDGER" liabilities:wallet:alice LIABILITY USD false)
BOB=$(acct "$LEDGER" liabilities:wallet:bob LIABILITY USD false)
FEES=$(acct "$LEDGER" revenue:fees REVENUE USD false)
FX_USD=$(acct "$LEDGER" assets:fx:usd ASSET USD true)
FX_EUR=$(acct "$LEDGER" assets:fx:eur ASSET EUR true)
ALICE_EUR=$(acct "$LEDGER" liabilities:wallet:alice:eur LIABILITY EUR false)
echo "created 7 accounts"

bold "Register a webhook (deliveries go to the webhook-sink container)"
req POST "/v1/ledgers/$LEDGER/webhooks" '{"url":"http://webhook-sink:8090/hooks"}' | jq -c '{id, event_types}'

bold "Deposit \$500 to Alice"
req POST "/v1/ledgers/$LEDGER/transactions" \
  "{\"description\":\"deposit\",\"entries\":[$(leg "$BANK" DEBIT 50000 USD),$(leg "$ALICE" CREDIT 50000 USD)]}" "$(key)" | jq -c '{id, status}'

bold "Alice pays Bob \$120 + \$1.20 fee, retried with the same Idempotency-Key"
IK=$(key)
BODY="{\"description\":\"p2p\",\"entries\":[$(leg "$ALICE" DEBIT 12120 USD),$(leg "$BOB" CREDIT 12000 USD),$(leg "$FEES" CREDIT 120 USD)]}"
FIRST=$(req POST "/v1/ledgers/$LEDGER/transactions" "$BODY" "$IK" | jq -r .id)
SECOND=$(curl -sS -D - -o /dev/null -X POST "$API/v1/ledgers/$LEDGER/transactions" -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -H "Idempotency-Key: $IK" -d "$BODY" | grep -i '^idempotent-replayed' || true)
echo "first: $FIRST  retry: ${SECOND:-<not replayed!>}"
bal "$ALICE"; bal "$BOB"; bal "$FEES"

bold "Overdraft attempt is rejected"
req POST "/v1/ledgers/$LEDGER/transactions" \
  "{\"entries\":[$(leg "$ALICE" DEBIT 99999999 USD),$(leg "$BOB" CREDIT 99999999 USD)]}" "$(key)" | jq -c '{status, code, detail}'

bold "Card authorization: hold \$80, capture \$64.50"
HOLD=$(req POST "/v1/ledgers/$LEDGER/transactions" \
  "{\"pending\":true,\"entries\":[$(leg "$ALICE" DEBIT 8000 USD),$(leg "$BOB" CREDIT 8000 USD)]}" "$(key)" | jq -r .id)
echo "while held:"; bal "$ALICE"
req POST "/v1/transactions/$HOLD/post" "{\"entries\":[$(leg "$ALICE" DEBIT 6450 USD),$(leg "$BOB" CREDIT 6450 USD)]}" "$(key)" | jq -c '{id, status}'
echo "after capture:"; bal "$ALICE"

bold "Refund the P2P payment via reversal"
req POST "/v1/transactions/$FIRST/reverse" '{"description":"refund"}' "$(key)" | jq -c '{id, reverses_id, status}'
bal "$ALICE"; bal "$BOB"

bold "Convert \$100 to EUR 92 through FX clearing accounts"
req POST "/v1/ledgers/$LEDGER/transactions" \
  "{\"metadata\":{\"rate\":\"0.92\"},\"entries\":[$(leg "$ALICE" DEBIT 10000 USD),$(leg "$FX_USD" CREDIT 10000 USD),$(leg "$FX_EUR" DEBIT 9200 EUR),$(leg "$ALICE_EUR" CREDIT 9200 EUR)]}" "$(key)" | jq -c '{id, status}'
bal "$ALICE"; bal "$ALICE_EUR"

bold "Alice's statement (newest first)"
req GET "/v1/accounts/$ALICE/entries?limit=10" | jq -r '.data[] | "v\(.account_version)\t\(.direction)\t\(.amount)\tbalance_after=\(.balance_after)\tpending=\(.pending)"'

bold "Alice's balance right after her deposit (point in time)"
AT=$(req GET "/v1/accounts/$ALICE/entries?limit=200" | jq -r '.data[-1].created_at')
req GET "/v1/accounts/$ALICE/balance?as_of=$(jq -rn --arg t "$AT" '$t|@uri')" | jq -c .

bold "Trial balance"
req GET "/v1/ledgers/$LEDGER/trial-balance" | jq -c '{balanced, currencies: [.currencies[] | {currency, total_debits, total_credits}]}'

bold "Webhook deliveries (from webhook-sink logs)"
sleep 3
docker compose -f "$(dirname "$0")/../deploy/docker-compose.yml" logs webhook-sink 2>/dev/null | grep -o '"type":"[a-z.]*"' | sort | uniq -c || true
