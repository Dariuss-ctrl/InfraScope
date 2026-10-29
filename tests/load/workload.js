// k6 reference workload for InfraScope (docs/benchmarks/README.md).
//
// Sends the shop request mix at a fixed arrival rate. Products, customers and
// orders are drawn from the ranges the demo service's deterministic seed
// creates, so every lookup hits existing data.
//
// Run with `make load RATE=20 DURATION=2m`.
import http from 'k6/http';
import { check } from 'k6';

const TARGET = __ENV.TARGET_URL || 'http://demo-service:8082';
const PRODUCTS = Number(__ENV.SEED_PRODUCTS || 10000);
const CUSTOMERS = Number(__ENV.SEED_CUSTOMERS || 100000);
const ORDERS = Number(__ENV.SEED_ORDERS || 1000000);

export const options = {
  scenarios: {
    shop: {
      // Open model: requests arrive at a fixed rate whether or not earlier
      // ones have finished, so a slow server cannot slow the load down.
      executor: 'constant-arrival-rate',
      rate: Number(__ENV.RATE || 20),
      timeUnit: '1s',
      duration: __ENV.DURATION || '2m',
      preAllocatedVUs: 20,
      maxVUs: 200,
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.01'],
    checks: ['rate>0.99'],
  },
};

const between = (lo, hi) => lo + Math.floor(Math.random() * (hi - lo + 1));
const customer = () => `customer${between(0, CUSTOMERS - 1)}@example.com`;

// [share, request]: shares sum to 1 and match the reference mix.
const mix = [
  [0.5, () => http.get(`${TARGET}/products?limit=20&offset=${between(0, PRODUCTS - 20)}`,
    { tags: { name: 'GET /products' } })],
  [0.2, () => http.get(`${TARGET}/products/${between(1, PRODUCTS)}`,
    { tags: { name: 'GET /products/{id}' } })],
  [0.15, () => http.get(`${TARGET}/orders?customer_email=${encodeURIComponent(customer())}`,
    { tags: { name: 'GET /orders?customer_email' } })],
  [0.1, () => http.post(`${TARGET}/orders`,
    JSON.stringify({ customer_email: customer(), product_id: between(1, PRODUCTS), quantity: between(1, 5) }),
    { headers: { 'Content-Type': 'application/json' }, tags: { name: 'POST /orders' } })],
  [0.05, () => http.get(`${TARGET}/orders/${between(1, ORDERS)}`,
    { tags: { name: 'GET /orders/{id}' } })],
];

function pickRequest() {
  let p = Math.random();
  for (const [share, send] of mix) {
    p -= share;
    if (p < 0) return send;
  }
  return mix[mix.length - 1][1]; // floating-point rounding can leave p at ~0
}

export default function () {
  const res = pickRequest()();
  check(res, { 'status is 2xx': (r) => r.status >= 200 && r.status < 300 });
}
