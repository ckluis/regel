-- regel catalog substrate DDL (ADR-03 §1, ADR-05 §2/§6, ADR-06 §5, ADR-04 §5,
-- STAGE-A-PLAN pin #10). Idempotent: safe to apply repeatedly. Table shapes are
-- verbatim from the ADRs; role grants are applied separately by Bootstrap using
-- the configured kernel role name.

-- btree_gist provides the '=' GiST operator class the I4 exclusion needs.
CREATE EXTENSION IF NOT EXISTS btree_gist;

-- (5) Admission ledger — one row per gate pass. Created first: definition,
-- name_pointer, and name_pointer_history all FK to admission(id).
CREATE TABLE IF NOT EXISTS admission (
  id               bigserial PRIMARY KEY,
  actor_kind       text NOT NULL CHECK (actor_kind IN ('engineer','tenant','agent','system')),
  actor_id         text NOT NULL,
  via              text NOT NULL CHECK (via IN ('cli','settings','mcp','git')),
  submitted_hashes text[] NOT NULL,
  verifier_report  jsonb NOT NULL,
  tsgo_ms          int,
  migration_sql    text,
  seeders          jsonb NOT NULL DEFAULT '[]',
  verdict_delta    jsonb,
  created_at       timestamptz NOT NULL DEFAULT now()
);

-- (1) Immortal content store. INSERT-only: UPDATE/DELETE revoked from the kernel
-- role by Bootstrap. Content-addressed, unpartitioned.
CREATE TABLE IF NOT EXISTS definition (
  hash            text PRIMARY KEY,
  ast_schema_ver  smallint NOT NULL,
  kind            text NOT NULL CHECK (kind IN
                    ('resource','function','component','view','policy',
                     'workflow','prompt','translation','type')),
  ast             bytea NOT NULL,
  canonical_text  text  NOT NULL,
  contracts       jsonb NOT NULL DEFAULT '[]',
  deps            text[] NOT NULL DEFAULT '{}',
  supersedes      text REFERENCES definition(hash),
  admission_id    bigint NOT NULL REFERENCES admission(id),
  created_at      timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT addr_shape CHECK (hash ~ '^r[0-9]+_[0-9a-z]+$')
);

-- (2) Out-of-hash metadata.
CREATE TABLE IF NOT EXISTS definition_meta (
  hash      text PRIMARY KEY REFERENCES definition(hash),
  docstring text,
  comments  jsonb NOT NULL DEFAULT '{}'
);

-- (3) Mutable scoped name pointer — the live catalog, the ONLY mutable code table.
CREATE TABLE IF NOT EXISTS name_pointer (
  name         text NOT NULL,
  scope_kind   smallint NOT NULL CHECK (scope_kind BETWEEN 0 AND 4),
  scope_id     text NOT NULL DEFAULT '',
  kind         text NOT NULL,
  visibility   text NOT NULL DEFAULT 'exported' CHECK (visibility IN ('exported','private')),
  hash         text NOT NULL REFERENCES definition(hash),
  overrides    text REFERENCES definition(hash),
  admission_id bigint NOT NULL REFERENCES admission(id),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (name, scope_kind, scope_id)
);

-- (4) Append-only temporal history, written by the I7 trigger. Unpartitioned so
-- the I4 GiST range-overlap exclusion is creatable (Postgres rejects exclusion
-- constraints on partitioned tables).
CREATE TABLE IF NOT EXISTS name_pointer_history (
  name       text NOT NULL, scope_kind smallint NOT NULL, scope_id text NOT NULL,
  hash       text NOT NULL REFERENCES definition(hash),
  valid_from timestamptz NOT NULL,
  valid_to   timestamptz,
  admission_id bigint NOT NULL REFERENCES admission(id),
  EXCLUDE USING gist (name WITH =, scope_kind WITH =, scope_id WITH =,
                      tstzrange(valid_from, valid_to) WITH &&)
);

-- (6) Gate + coverage ledgers.
CREATE TABLE IF NOT EXISTS gate_refusal (
  refusal_id       uuid PRIMARY KEY,
  principal        text NOT NULL,
  scope_attempted  text,
  submitted_hashes text[],
  outcome          text NOT NULL CHECK (outcome IN
                     ('rejected','stale-base','retry-exhausted','budget-exhausted','busy')),
  verdict          jsonb NOT NULL,
  created_at       timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS verifier_coverage (
  epoch int NOT NULL, component text NOT NULL,
  threat_class_ids text[] NOT NULL, corpus_case_count int NOT NULL,
  mutation_score numeric NOT NULL,
  PRIMARY KEY (epoch, component)
);
CREATE TABLE IF NOT EXISTS perf_budget (
  epoch int NOT NULL, metric text NOT NULL, tier text,
  budget numeric NOT NULL, measured numeric, milestone text NOT NULL,
  PRIMARY KEY (epoch, metric)
);
CREATE TABLE IF NOT EXISTS continuation_coverage (
  epoch int NOT NULL, frame_kind text NOT NULL, cfr_version int NOT NULL,
  decoder text NOT NULL, covered bool NOT NULL,
  PRIMARY KEY (epoch, frame_kind, cfr_version, decoder)
);

-- ADR-05 §2 continuation store.
CREATE TABLE IF NOT EXISTS continuation (
  id            uuid PRIMARY KEY,
  kind          text NOT NULL CHECK (kind IN ('workflow','session','request')),
  root_def_hash text NOT NULL REFERENCES definition(hash),
  epoch         int  NOT NULL,
  format_ver    int  NOT NULL,
  frames        bytea NOT NULL,
  wake          jsonb NOT NULL,
  status        text NOT NULL CHECK (status IN
                  ('sleeping','ready','running','condition','done','failed')),
  step_seq      bigint NOT NULL DEFAULT 0,
  lease_owner   uuid,
  lease_until   timestamptz,
  principal     jsonb NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT wake_kind_shape CHECK (
    wake ? 'kind' AND wake->>'kind' IN ('timer','message','event','join','manual'))
);
-- Partial timer index for the sleeping-timer wake scan. Indexed on the raw
-- wake->>'due' TEXT (not (…)::timestamptz): the text→timestamptz cast is STABLE,
-- not IMMUTABLE, so Postgres rejects it in an index expression (42P17). ADR-05
-- 'due' is a canonical ISO-8601 UTC string, so lexical order == chronological
-- order and range scans (wake->>'due' <= :now_iso) are still index-served.
CREATE INDEX IF NOT EXISTS continuation_timer_idx ON continuation ((wake->>'due'))
  WHERE status = 'sleeping' AND wake->>'kind' = 'timer';

-- ADR-05 §6 durable conditions + restarts.
CREATE TABLE IF NOT EXISTS durable_condition (
  id              uuid PRIMARY KEY,
  continuation_id uuid NOT NULL REFERENCES continuation(id),
  class           text NOT NULL,
  payload         jsonb NOT NULL,
  signaled_at     timestamptz NOT NULL DEFAULT now(),
  status          text NOT NULL DEFAULT 'open' CHECK (status IN ('open','resolved')),
  resolved_restart uuid, resolved_args jsonb, resolved_by text, resolved_at timestamptz,
  CONSTRAINT class_shape CHECK (class ~ '^[a-z][a-z0-9]*(\.[a-z0-9]+)*$'),
  CONSTRAINT resolved_consistency CHECK (
    (status = 'resolved') =
      (resolved_restart IS NOT NULL AND resolved_by IS NOT NULL AND resolved_at IS NOT NULL))
);
CREATE TABLE IF NOT EXISTS restart (
  id            uuid PRIMARY KEY,
  condition_id  uuid NOT NULL REFERENCES durable_condition(id),
  name          text NOT NULL,
  label         text NOT NULL,
  params_schema jsonb NOT NULL DEFAULT '{}',
  capability_required text
);
-- Deferred FK: resolution can no longer name a nonexistent restart.
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'resolved_restart_fk') THEN
    ALTER TABLE durable_condition
      ADD CONSTRAINT resolved_restart_fk
      FOREIGN KEY (resolved_restart) REFERENCES restart(id);
  END IF;
END $$;

-- ADR-06 §5 unified task table.
CREATE TABLE IF NOT EXISTS task (
  id           uuid PRIMARY KEY,
  kind         text NOT NULL CHECK (kind IN ('resume','cron','deliver')),
  run_at       timestamptz NOT NULL,
  payload      jsonb NOT NULL,
  status       text NOT NULL DEFAULT 'ready' CHECK (status IN ('ready','running','done','dead')),
  attempts     int NOT NULL DEFAULT 0,
  lease_owner  uuid,
  lease_until  timestamptz,
  created_at   timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT payload_shape CHECK (
    (kind = 'resume'  AND payload ? 'continuation_id' AND payload ? 'step_seq') OR
    (kind = 'cron'    AND payload ? 'schedule'        AND payload ? 'target')   OR
    (kind = 'deliver' AND payload ? 'intent_id'       AND payload ? 'dedup_key'))
);
CREATE INDEX IF NOT EXISTS task_ready_idx ON task (run_at) WHERE status = 'ready';

-- ADR-04 §5 capability grant rows (GRANT is reserved, so grant_row).
CREATE TABLE IF NOT EXISTS grant_row (
  subject      text NOT NULL,
  capability   text NOT NULL,
  scope        text NOT NULL,
  expires_at   timestamptz,
  granted_by   text NOT NULL,
  admission_id bigint REFERENCES admission(id),
  PRIMARY KEY (subject, capability, scope)
);

-- STAGE-A-PLAN pin #10 minimal epoch table (ADR-10/ADR-08).
CREATE TABLE IF NOT EXISTS epoch (
  n                    int PRIMARY KEY,
  std_manifest_root    text NOT NULL,
  dispatch_attestation text NOT NULL,
  created_at           timestamptz NOT NULL DEFAULT now()
);

-- Bootstrap bookkeeping.
CREATE TABLE IF NOT EXISTS schema_version (
  version    int PRIMARY KEY,
  applied_at timestamptz NOT NULL DEFAULT now()
);

-- I2: validate every element of definition.deps against definition(hash) inside
-- the transaction. Rows inserted earlier in the same admission are visible.
CREATE OR REPLACE FUNCTION regel_validate_deps() RETURNS trigger AS $$
DECLARE d text;
BEGIN
  FOREACH d IN ARRAY NEW.deps LOOP
    IF NOT EXISTS (SELECT 1 FROM definition WHERE hash = d) THEN
      RAISE EXCEPTION 'dangling dependency edge: %', d
        USING ERRCODE = 'foreign_key_violation';
    END IF;
  END LOOP;
  RETURN NEW;
END; $$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS definition_validate_deps ON definition;
CREATE TRIGGER definition_validate_deps
  BEFORE INSERT ON definition
  FOR EACH ROW EXECUTE FUNCTION regel_validate_deps();

-- I7: name_pointer history writer. SECURITY DEFINER so it runs as the table
-- owner — application code (the kernel role) never writes history directly, and
-- one captured timestamp keeps the closing and opening windows exactly adjacent
-- (no gap, no overlap).
CREATE OR REPLACE FUNCTION regel_write_history() RETURNS trigger
SECURITY DEFINER AS $$
DECLARE ts timestamptz := clock_timestamp();
BEGIN
  IF TG_OP = 'UPDATE' THEN
    UPDATE name_pointer_history
       SET valid_to = ts
     WHERE name = OLD.name AND scope_kind = OLD.scope_kind
       AND scope_id = OLD.scope_id AND valid_to IS NULL;
  END IF;
  INSERT INTO name_pointer_history
    (name, scope_kind, scope_id, hash, valid_from, valid_to, admission_id)
  VALUES (NEW.name, NEW.scope_kind, NEW.scope_id, NEW.hash, ts, NULL, NEW.admission_id);
  RETURN NEW;
END; $$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS name_pointer_history_writer ON name_pointer;
CREATE TRIGGER name_pointer_history_writer
  BEFORE INSERT OR UPDATE ON name_pointer
  FOR EACH ROW EXECUTE FUNCTION regel_write_history();
