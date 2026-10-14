// k6 load test for the posting path.
//
//   MODE=uniform  random wallet-to-wallet transfers across many accounts
//                 (low contention: measures raw posting throughput)
//   MODE=hot      every transfer also credits one shared fee account
//                 (worst case: all writers serialise on a single row lock)
//
// Run: make loadtest MODE=uniform VUS=50 DURATION=30s
import http from 'k6/http';
import { check } from 'k6';

const API = __ENV.API || 'http://localhost:8080';
const KEY = __ENV.ADMIN_API_KEY || 'dev-admin-key-change-me';
const MODE = __ENV.MODE || 'uniform';
const WALLETS = Number(__ENV.WALLETS || 1000);

export const options = {
  scenarios: {
    transfers: {
      executor: 'constant-vus',
      vus: Number(__ENV.VUS || 50),
      duration: __ENV.DURATION || '30s',
    },
  },
  setupTimeout: '120s',
  thresholds: {
    'http_req_failed{type:transfer}': ['rate<0.01'],
    'checks{type:transfer}': ['rate>0.99'],
    // Surfaces transfer-only latency in the summary (setup requests excluded).
    'http_req_duration{type:transfer}': ['p(99)<2000'],
  },
  summaryTrendStats: ['avg', 'p(50)', 'p(95)', 'p(99)', 'max'],
};

const headers = (extra = {}) => ({
  headers: Object.assign({ Authorization: `Bearer ${KEY}`, 'Content-Type': 'application/json' }, extra),
});

function uuid() {
  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (c) => {
    const r = (Math.random() * 16) | 0;
    return (c === 'x' ? r : (r & 0x3) | 0x8).toString(16);
  });
}

function post(path, body, tags, idempotent) {
  const h = idempotent ? headers({ 'Idempotency-Key': uuid() }) : headers();
  h.tags = tags;
  return http.post(`${API}${path}`, JSON.stringify(body), h);
}

function account(ledger, code, type, allowNegative) {
  const r = post(`/v1/ledgers/${ledger}/accounts`, { code, name: code, type, currency: 'USD', allow_negative: allowNegative }, { type: 'setup' });
  if (r.status !== 201) throw new Error(`create account: ${r.status} ${r.body}`);
  return r.json('id');
}

export function setup() {
  const ledger = post('/v1/ledgers', { name: `loadtest-${MODE}` }, { type: 'setup' }).json('id');
  const bank = account(ledger, 'bank', 'ASSET', true);
  const fees = account(ledger, 'fees', 'REVENUE', false);
  const wallets = [];
  for (let i = 0; i < WALLETS; i++) wallets.push(account(ledger, `wallet:${i}`, 'LIABILITY', false));

  // Fund every wallet with $10,000 using atomic batches of 100.
  for (let i = 0; i < wallets.length; i += 100) {
    const transactions = wallets.slice(i, i + 100).map((w) => ({
      entries: [
        { account_id: bank, direction: 'DEBIT', amount: '1000000', currency: 'USD' },
        { account_id: w, direction: 'CREDIT', amount: '1000000', currency: 'USD' },
      ],
    }));
    const r = post(`/v1/ledgers/${ledger}/transactions/batch`, { transactions }, { type: 'setup' }, true);
    if (r.status !== 201) throw new Error(`fund: ${r.status} ${r.body}`);
  }
  return { ledger, fees, wallets };
}

export default function (d) {
  const i = Math.floor(Math.random() * d.wallets.length);
  let j = Math.floor(Math.random() * d.wallets.length);
  if (j === i) j = (j + 1) % d.wallets.length;
  const amount = 1 + Math.floor(Math.random() * 100);

  const entries = [
    { account_id: d.wallets[i], direction: 'DEBIT', amount: String(amount + (MODE === 'hot' ? 1 : 0)), currency: 'USD' },
    { account_id: d.wallets[j], direction: 'CREDIT', amount: String(amount), currency: 'USD' },
  ];
  if (MODE === 'hot') {
    entries.push({ account_id: d.fees, direction: 'CREDIT', amount: '1', currency: 'USD' });
  }
  const r = post(`/v1/ledgers/${d.ledger}/transactions`, { entries }, { type: 'transfer' }, true);
  check(r, { 'transfer committed': (res) => res.status === 201 }, { type: 'transfer' });
}

export function teardown(d) {
  const r = http.get(`${API}/v1/ledgers/${d.ledger}/trial-balance`, headers());
  check(r, { 'trial balance holds after load': (res) => res.json('balanced') === true });
  console.log(`trial balance: ${r.body}`);
}
