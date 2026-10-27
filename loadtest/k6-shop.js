// Load profile close to production: 85% catalog reads, 15% order writes.
//   k6 run -e BASE_URL=http://localhost loadtest/k6-shop.js
//   k6 run -e BASE_URL=https://dev.shop.example.com -e PEAK_RPS=2000 loadtest/k6-shop.js
// Thresholds mirror the SLOs, so a run fails exactly when the SLO would be violated.
import http from 'k6/http';
import { check } from 'k6';
import { Counter } from 'k6/metrics';

const CATALOG = __ENV.CATALOG_URL || `${__ENV.BASE_URL || 'http://localhost:8080'}`;
const ORDERS = __ENV.ORDERS_URL || `${__ENV.BASE_URL || 'http://localhost:8081'}`;
const PEAK = Number(__ENV.PEAK_RPS || 300);

const ordersCreated = new Counter('orders_created');

export const options = {
  discardResponseBodies: false,
  scenarios: {
    browse: {
      executor: 'ramping-arrival-rate',
      exec: 'browse',
      startRate: 10,
      timeUnit: '1s',
      preAllocatedVUs: 50,
      maxVUs: 500,
      stages: [
        { target: Math.round(PEAK * 0.85), duration: '1m' },
        { target: Math.round(PEAK * 0.85), duration: '3m' },
        { target: 0, duration: '30s' },
      ],
    },
    checkout: {
      executor: 'ramping-arrival-rate',
      exec: 'checkout',
      startRate: 1,
      timeUnit: '1s',
      preAllocatedVUs: 20,
      maxVUs: 200,
      stages: [
        { target: Math.round(PEAK * 0.15), duration: '1m' },
        { target: Math.round(PEAK * 0.15), duration: '3m' },
        { target: 0, duration: '30s' },
      ],
    },
  },
  thresholds: {
    'http_req_failed': ['rate<0.001'],                          // availability SLO 99.9%
    'http_req_duration{scenario:browse}': ['p(99)<300'],        // latency SLO
    'http_req_duration{scenario:checkout}': ['p(99)<500'],
  },
};

const products = ['p-1001', 'p-1002', 'p-1003', 'p-2001', 'p-2002', 'p-3001', 'p-3002'];
const pick = (a) => a[Math.floor(Math.random() * a.length)];

export function browse() {
  if (Math.random() < 0.3) {
    const res = http.get(`${CATALOG}/api/v1/products?category=electronics&limit=20`, { tags: { name: 'list' } });
    check(res, { 'list 200': (r) => r.status === 200 });
  } else {
    const res = http.get(`${CATALOG}/api/v1/products/${pick(products)}`, { tags: { name: 'get' } });
    check(res, { 'get 200': (r) => r.status === 200 });
  }
}

export function checkout() {
  const body = JSON.stringify({
    customer_id: `c-${__VU}`,
    items: [
      { product_id: pick(products), quantity: 1 + Math.floor(Math.random() * 3), price_rub: 1990 },
      { product_id: pick(products), quantity: 1, price_rub: 590 },
    ],
  });
  const res = http.post(`${ORDERS}/api/v1/orders`, body, {
    headers: { 'Content-Type': 'application/json', 'Idempotency-Key': `${__VU}-${__ITER}-${Date.now()}` },
    tags: { name: 'create_order' },
  });
  if (check(res, { 'order 201': (r) => r.status === 201 })) {
    ordersCreated.add(1);
  }
}
