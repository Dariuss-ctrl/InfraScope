// k6 workload for InfraScope (docs/benchmarks/README.md).
//
// Phase 0 exercises /healthz only, so the load harness itself can be verified
// end to end. Phase 1 replaces the request mix with the shop endpoints from
// the reference workload.
//
// Run with `make load RATE=20 DURATION=2m`.
import http from 'k6/http';
import { check } from 'k6';

const TARGET = __ENV.TARGET_URL || 'http://demo-service:8082';

export const options = {
  scenarios: {
    healthz: {
      // Open model: requests arrive at a fixed rate whether or not earlier
      // ones have finished, so a slow server cannot slow the load down.
      executor: 'constant-arrival-rate',
      rate: Number(__ENV.RATE || 20),
      timeUnit: '1s',
      duration: __ENV.DURATION || '2m',
      preAllocatedVUs: 10,
      maxVUs: 50,
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.01'],
    checks: ['rate>0.99'],
  },
};

export default function () {
  const res = http.get(`${TARGET}/healthz`, { tags: { name: 'GET /healthz' } });
  check(res, { 'status is 200': (r) => r.status === 200 });
}
