-- DeployRadar: deployments reported by provider webhooks, and the before/after
-- health comparison made for each successful one (SPEC FR-9, ADR-0007).
-- Metric aggregates stay in Prometheus; only the compared values and the exact
-- queries behind them are stored here.

-- Every verified webhook delivery is stored before it is processed, so a
-- duplicate delivery ID is recognised even when the first attempt failed.
CREATE TABLE webhook_deliveries (
    delivery_id  text        PRIMARY KEY CHECK (length(delivery_id) BETWEEN 1 AND 128),
    event        text        NOT NULL CHECK (length(event) BETWEEN 1 AND 64),
    payload      jsonb       NOT NULL,
    received_at  timestamptz NOT NULL DEFAULT now(),
    outcome      text        NOT NULL DEFAULT 'pending'
                             CHECK (outcome IN ('pending', 'applied', 'ignored', 'failed')),
    error        text,
    processed_at timestamptz
);

CREATE INDEX webhook_deliveries_received_at_idx ON webhook_deliveries (received_at);

-- One row per provider deployment. service and environment hold the values of
-- the Prometheus labels service_name and deployment_environment_name, so a
-- deployment is matched to its metrics without a registry table.
CREATE TABLE deployments (
    id                     bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    provider               text        NOT NULL CHECK (provider IN ('github')),
    repository             text        NOT NULL CHECK (length(repository) BETWEEN 1 AND 256),
    provider_deployment_id text        NOT NULL CHECK (length(provider_deployment_id) BETWEEN 1 AND 64),
    service                text        NOT NULL CHECK (length(service) BETWEEN 1 AND 128),
    environment            text        NOT NULL CHECK (length(environment) BETWEEN 1 AND 128),
    ref                    text        NOT NULL CHECK (length(ref) BETWEEN 1 AND 256),
    sha                    text        NOT NULL CHECK (sha ~ '^[0-9a-f]{7,64}$'),
    creator                text        NOT NULL DEFAULT '' CHECK (length(creator) <= 128),
    description            text        NOT NULL DEFAULT '' CHECK (length(description) <= 1024),
    created_at             timestamptz NOT NULL,
    state                  text        NOT NULL CHECK (state IN
                               ('pending', 'queued', 'in_progress', 'success', 'failure', 'error', 'inactive')),
    -- When the status happened at the provider, not when its webhook arrived.
    -- Out-of-order deliveries are resolved on this time.
    state_at               timestamptz NOT NULL,
    -- The first terminal status. A later terminal status does not move it.
    finished_at            timestamptz,
    updated_at             timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider, provider_deployment_id)
);

CREATE INDEX deployments_service_environment_idx ON deployments (service, environment, id DESC);

-- Append-only history of every status delivered, whether or not it changed
-- the deployment's state.
CREATE TABLE deployment_changes (
    id            bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    deployment_id bigint      NOT NULL REFERENCES deployments (id) ON DELETE CASCADE,
    delivery_id   text        NOT NULL REFERENCES webhook_deliveries (delivery_id),
    state         text        NOT NULL CHECK (state IN
                      ('pending', 'queued', 'in_progress', 'success', 'failure', 'error', 'inactive')),
    state_at      timestamptz NOT NULL,
    -- false when the precedence rule kept the existing state.
    applied       boolean     NOT NULL,
    recorded_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX deployment_changes_deployment_id_idx ON deployment_changes (deployment_id, state_at, id);

-- One comparison per deployment, queued when it first succeeds and computed
-- once the after window has passed. Counts are doubles because Prometheus
-- increase() extrapolates. The error rate is derived from the counts when
-- read, so it always reconciles with them.
CREATE TABLE deployment_comparisons (
    id                 bigint           GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    deployment_id      bigint           NOT NULL UNIQUE REFERENCES deployments (id) ON DELETE CASCADE,
    status             text             NOT NULL DEFAULT 'pending'
                                        CHECK (status IN ('pending', 'done', 'failed')),
    attempts           integer          NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    -- Set when a worker claims the row; another worker may reclaim it once
    -- the lease has passed, so a crashed worker does not strand it.
    claimed_at         timestamptz,
    before_from        timestamptz      NOT NULL,
    before_to          timestamptz      NOT NULL,
    after_from         timestamptz      NOT NULL,
    after_to           timestamptz      NOT NULL,
    before_requests    double precision CHECK (before_requests >= 0),
    before_errors      double precision CHECK (before_errors >= 0),
    after_requests     double precision CHECK (after_requests >= 0),
    after_errors       double precision CHECK (after_errors >= 0),
    before_p95_seconds double precision CHECK (before_p95_seconds >= 0),
    after_p95_seconds  double precision CHECK (after_p95_seconds >= 0),
    error_rate_verdict text             CHECK (error_rate_verdict IN
                                            ('regression', 'improvement', 'no_change', 'insufficient_data')),
    p95_verdict        text             CHECK (p95_verdict IN
                                            ('regression', 'improvement', 'no_change', 'insufficient_data')),
    verdict            text             CHECK (verdict IN
                                            ('regression', 'improvement', 'no_change', 'insufficient_data')),
    confidence         double precision CHECK (confidence BETWEEN 0 AND 1),
    reasons            text[]           NOT NULL DEFAULT '{}',
    -- The exact PromQL, evaluation time and range behind every stored value.
    queries            jsonb            NOT NULL DEFAULT '{}',
    method_version     integer          NOT NULL DEFAULT 1,
    error              text,
    computed_at        timestamptz,
    created_at         timestamptz      NOT NULL DEFAULT now(),
    updated_at         timestamptz      NOT NULL DEFAULT now(),
    CHECK (before_from < before_to AND before_to <= after_from AND after_from < after_to),
    CHECK ((status = 'done') = (verdict IS NOT NULL AND computed_at IS NOT NULL))
);

-- The worker's claim query: pending rows whose after window has passed.
CREATE INDEX deployment_comparisons_due_idx ON deployment_comparisons (after_to) WHERE status = 'pending';
