-- DeployRadar: per-route evidence for each comparison, so a verdict can say
-- which endpoint changed (ADR-0007). Each element is one http_route's
-- requests and 5xx responses in both windows, worst change first. Like the
-- totals, these are values Prometheus measured, stored with their queries.

ALTER TABLE deployment_comparisons
    ADD COLUMN routes jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(routes) = 'array');
