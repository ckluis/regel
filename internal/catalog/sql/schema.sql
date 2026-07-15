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
  -- BUILD-A (ADR-03 §1 table 4): visibility snapshotted per window so as-of
  -- resolution can carry the identical R1-12 visibility predicate as live.
  visibility text NOT NULL DEFAULT 'exported' CHECK (visibility IN ('exported','private')),
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

-- (7) Admission-spam control (ADR-12 §5, ADR-07 §3 R1-07). BUILD-C (increment
-- C4): the per-principal admission-fuel token bucket and the per-agent-kind
-- capacity table the pre-BEGIN ADMISSION_BUDGET (budget-exhausted) refusal path
-- checks and charges. Separate from evaluation fuel. admission_capacity.derived_from
-- is 'provisional' until the ADR-12 §5 eval P95 sizes it (R1-13); the MCP
-- increment re-derives it every epoch from the then-current P95.
CREATE TABLE IF NOT EXISTS admission_capacity (
  agent_kind     text PRIMARY KEY,
  capacity       numeric NOT NULL,
  refill_per_sec numeric NOT NULL,
  derived_from   text NOT NULL DEFAULT 'provisional'
);
CREATE TABLE IF NOT EXISTS admission_fuel (
  principal      text PRIMARY KEY,
  capacity       numeric NOT NULL,
  tokens         numeric NOT NULL,
  refill_per_sec numeric NOT NULL,
  updated_at     timestamptz NOT NULL DEFAULT now()
);
-- Provisional per-kind capacities (derived_from='provisional'): humans/system are
-- effectively unmetered; the agent kind carries the spam-control bucket the MCP
-- increment re-derives from eval P95. Seeded idempotently.
INSERT INTO admission_capacity (agent_kind, capacity, refill_per_sec, derived_from) VALUES
  ('engineer', 10000000, 1000000, 'provisional'),
  ('tenant',   10000000, 1000000, 'provisional'),
  ('system',   10000000, 1000000, 'provisional'),
  ('agent',    100000,   1000,    'provisional')
ON CONFLICT (agent_kind) DO NOTHING;

-- (8) Derivation tier (BUILD-C: ADR-03 §1 table 8; ADR-07 §1 step 5a + V3/V6).
-- derived_resource records the last-admitted derived SHAPE per (resource, scope)
-- so the schema pass diffs the proposed shape against the recorded one (never
-- information_schema). derived_artifact records the INSPECTABLE proposed rows per
-- derivation pass, queried by V3/V6 in-transaction and served by C4 later.
CREATE TABLE IF NOT EXISTS derived_resource (
  resource_name text NOT NULL,
  scope_kind    smallint NOT NULL CHECK (scope_kind BETWEEN 0 AND 4),
  scope_id      text NOT NULL DEFAULT '',
  def_hash      text NOT NULL REFERENCES definition(hash),
  fields        jsonb NOT NULL,
  policy_name   text,
  table_name    text NOT NULL,
  admission_id  bigint NOT NULL REFERENCES admission(id),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (resource_name, scope_kind, scope_id)
);
CREATE TABLE IF NOT EXISTS derived_artifact (
  id            bigserial PRIMARY KEY,
  admission_id  bigint NOT NULL REFERENCES admission(id),
  resource_name text NOT NULL,
  scope_kind    smallint NOT NULL CHECK (scope_kind BETWEEN 0 AND 4),
  scope_id      text NOT NULL DEFAULT '',
  -- BUILD-D (ADR-10 §4): the ten-pass derivation roster + the D2 render 'template'
  -- pass (ADR-11 §1 static/dynamic split, an ADR-07 step-5a derivation). The
  -- Stage-C set (schema/policy/retire/validator) is extended with the seven
  -- remaining ADR-10 §4 passes (history, vault, horizon, components, openapi,
  -- mcptools, catalog) and the ADR-11 render 'template'.
  pass          text NOT NULL CHECK (pass IN
                  ('schema','policy','retire','validator',
                   'history','vault','horizon','components','openapi','mcptools','catalog',
                   'template')),
  detail        jsonb NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS derived_artifact_resource_idx
  ON derived_artifact (resource_name, scope_kind, scope_id);
-- Bring a pre-existing derived_artifact CHECK up to the BUILD-D + D2 pass roster
-- (detection keys on the newest member, 'template', so a Stage-C or a
-- BUILD-D-without-template constraint is both upgraded).
DO $$
DECLARE cn text;
BEGIN
  SELECT c.conname INTO cn FROM pg_constraint c
  WHERE c.conrelid = 'derived_artifact'::regclass AND c.contype = 'c'
    AND pg_get_constraintdef(c.oid) LIKE '%pass%'
    AND pg_get_constraintdef(c.oid) NOT LIKE '%template%';
  IF cn IS NOT NULL THEN
    EXECUTE 'ALTER TABLE derived_artifact DROP CONSTRAINT ' || quote_ident(cn);
    ALTER TABLE derived_artifact ADD CONSTRAINT derived_artifact_pass_check CHECK (pass IN
      ('schema','policy','retire','validator',
       'history','vault','horizon','components','openapi','mcptools','catalog',
       'template'));
  END IF;
END $$;

-- (8b) Vault substrate (BUILD-D, ADR-10 §4 item 5 / §5 pii modifier). A pii field's
-- value NEVER lands in the derived base table nor its history — it is AES-256-GCM
-- sealed under a PER-SUBJECT key token and stored ciphertext-only here, keyed by
-- (resource, subject_id[, field]). Shared tables (not one per field): the derivation
-- emits a per-resource `vault` ROUTE artifact naming which fields route here.
CREATE TABLE IF NOT EXISTS vault (
  resource   text NOT NULL,
  subject_id text NOT NULL,          -- the base row's id, as text
  field      text NOT NULL,
  ciphertext text NOT NULL,          -- hex(nonce ‖ AES-256-GCM ciphertext)
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (resource, subject_id, field)
);
-- Per-subject key row: the opaque key_token feeds the AEAD KDF (std/crypto §3). The
-- key material is never a dialect value; deleting this row is CRYPTO-SHRED — the
-- ciphertext above becomes permanently undecryptable (the subject's key is gone).
CREATE TABLE IF NOT EXISTS vault_key (
  resource   text NOT NULL,
  subject_id text NOT NULL,
  key_token  text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (resource, subject_id)
);
-- Crypto-shred audit spine: one append-only row per shredded subject (ADR-10 §4
-- item 5 "writes an attestation row").
CREATE TABLE IF NOT EXISTS shred_attestation (
  id          bigserial PRIMARY KEY,
  resource    text NOT NULL,
  subject_id  text NOT NULL,
  keys_shredded int NOT NULL,
  shredded_by text NOT NULL,
  shredded_at timestamptz NOT NULL DEFAULT now()
);

-- (8c) Reveal audit spine (BUILD-D D2, ADR-11 §8). Every RENDER-time reveal of a
-- masking-leaf slot under a live grant writes one append-only row here: the
-- plaintext appears only in the transient frame, but the ACT of revealing is
-- durably audited. Distinct from shred_attestation (which records key destruction).
CREATE TABLE IF NOT EXISTS reveal_audit (
  id          bigserial PRIMARY KEY,
  resource    text NOT NULL,
  subject_id  text NOT NULL,          -- the data subject (base row id)
  field       text NOT NULL,
  principal   text NOT NULL,          -- the render principal holding the grant
  grant_scope text NOT NULL,          -- the grant_row.scope consumed (resource|subject|field)
  revealed_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS reveal_audit_subject_idx
  ON reveal_audit (resource, subject_id, field);

-- ADR-05 §2 continuation store.
CREATE TABLE IF NOT EXISTS continuation (
  id            uuid PRIMARY KEY,
  kind          text NOT NULL CHECK (kind IN ('workflow','session','request')),
  root_def_hash text NOT NULL REFERENCES definition(hash),
  epoch         int  NOT NULL,
  format_ver    int  NOT NULL,
  frames        bytea NOT NULL,
  result        bytea,                       -- BUILD-B: terminal value, CFR value-encoded; NULL until done.
  wake          jsonb NOT NULL,
  status        text NOT NULL CHECK (status IN
                  ('sleeping','ready','running','condition','done','failed','cancelled')),
                -- BUILD-B: 'cancelled' for §5 race losers.
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

-- BUILD-B: bring a pre-existing continuation table up to the current shape.
-- The result column and the 'cancelled' status are additive; both are no-ops on
-- a fresh DB (the CREATE above already carries them).
ALTER TABLE continuation ADD COLUMN IF NOT EXISTS result bytea;
DO $$
DECLARE cn text;
BEGIN
  SELECT c.conname INTO cn
  FROM pg_constraint c
  WHERE c.conrelid = 'continuation'::regclass
    AND c.contype = 'c'
    AND pg_get_constraintdef(c.oid) LIKE '%status%'
    AND pg_get_constraintdef(c.oid) NOT LIKE '%cancelled%';
  IF cn IS NOT NULL THEN
    EXECUTE 'ALTER TABLE continuation DROP CONSTRAINT ' || quote_ident(cn);
    ALTER TABLE continuation ADD CONSTRAINT continuation_status_check CHECK (status IN
      ('sleeping','ready','running','condition','done','failed','cancelled'));
  END IF;
END $$;

-- ADR-05 §5 BUILD-B: channel messages. A receive claims the oldest undelivered
-- message on its channel; a send claims the oldest matching sleeping receiver.
CREATE TABLE IF NOT EXISTS channel_message (
  id         uuid PRIMARY KEY,
  channel    text  NOT NULL,
  payload    bytea NOT NULL,                          -- CFR value-encoded (EncodeValue)
  sent_by    text  NOT NULL,                          -- continuation id or external principal
  sent_at    timestamptz NOT NULL DEFAULT now(),
  claimed_by uuid REFERENCES continuation(id)         -- receiving continuation; NULL = undelivered
);
CREATE INDEX IF NOT EXISTS channel_message_undelivered_idx
  ON channel_message (channel) WHERE claimed_by IS NULL;
CREATE INDEX IF NOT EXISTS channel_message_fifo_idx
  ON channel_message (channel, sent_at) WHERE claimed_by IS NULL;

-- ADR-05 §7 BUILD-B: the transactional outbox. UNIQUE (continuation_id, step_seq,
-- ordinal) is THE dedup key that makes "effect fires exactly once" a DB fact.
CREATE TABLE IF NOT EXISTS outbox (
  id              uuid PRIMARY KEY,
  continuation_id uuid  NOT NULL REFERENCES continuation(id),
  step_seq        bigint NOT NULL,
  ordinal         int    NOT NULL,                     -- position within the step's effect trace
  class           text   NOT NULL,                     -- 'mail.send' | 'channel.send' | …
  payload         jsonb  NOT NULL,
  delivered_at    timestamptz,                         -- NULL until ADR-06's dispatcher delivers
  created_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (continuation_id, step_seq, ordinal)
);

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
-- BUILD-C (ADR-12 §4 layer 1): a reveal grant may name only a human principal — a
-- database CHECK plus the mint flow (§7) offers approvers only, so no agent
-- principal can hold the grant a masking leaf requires. Vault plaintext is thus
-- structurally unreachable from the agent plane at the grant layer.
CREATE TABLE IF NOT EXISTS grant_row (
  subject      text NOT NULL,
  capability   text NOT NULL,
  scope        text NOT NULL,
  expires_at   timestamptz,
  granted_by   text NOT NULL,
  admission_id bigint REFERENCES admission(id),
  PRIMARY KEY (subject, capability, scope),
  CONSTRAINT reveal_grant_human_only
    CHECK (capability <> 'pii.reveal' OR subject NOT LIKE 'agent:%')
);
-- Bring a pre-existing grant_row up to the reveal-grant CHECK (idempotent).
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'reveal_grant_human_only') THEN
    ALTER TABLE grant_row ADD CONSTRAINT reveal_grant_human_only
      CHECK (capability <> 'pii.reveal' OR subject NOT LIKE 'agent:%');
  END IF;
END $$;

-- (9) Agent-plane auth + approval (ADR-12 §1/§6, BUILD-C: DDL authored in ADR-03
-- §1 table 7 for approval_token; agent_key is the ADR-12 §1 "key hash → principal
-- binding table if needed", authored here with its BUILD-C marker in ADR-03).
--
-- agent_key binds an API key hash to a principal + its overlay (sandbox org)
-- scope. An agent is an ordinary capability principal (§1); the key IS a handle to
-- a grant_row bundle. Rotation is set-revoked (or delete): the next request the
-- key makes is refused, while past admissions stay attributed because every
-- admission carries the principal id as of the act.
CREATE TABLE IF NOT EXISTS agent_key (
  key_hash    text PRIMARY KEY,        -- sha256 hex of the presented API key
  actor_kind  text NOT NULL DEFAULT 'agent',
  actor_id    text NOT NULL,
  scope_kind  smallint NOT NULL DEFAULT 2 CHECK (scope_kind BETWEEN 0 AND 4),
  scope_id    text NOT NULL DEFAULT '', -- the agent's sandbox/overlay org id
  revoked     bool NOT NULL DEFAULT false,
  created_at  timestamptz NOT NULL DEFAULT now()
);

-- approval_token (ADR-03 §1 table 7 / ADR-12 §6): one-shot product-scope approval.
-- Minted by a human product-write holder against a patch's EXACT content hashes;
-- consumed inside the admission transaction (one-shot CAS on consumed_by); a token
-- whose bound hashes no longer match the submission is dead. scope_attempted binds
-- the token to the product scope it authorizes.
CREATE TABLE IF NOT EXISTS approval_token (
  token           uuid PRIMARY KEY,
  bound_hashes    text[] NOT NULL,     -- exact content hashes the human approved (sorted)
  minted_by       text NOT NULL,       -- approving human principal (product-write holder)
  minted_for      text NOT NULL,       -- author agent principal ("agent:id")
  scope_attempted text NOT NULL DEFAULT '0:', -- the "kind:id" scope this authorizes
  expires_at      timestamptz NOT NULL,
  consumed_by     bigint REFERENCES admission(id),  -- set by the consuming admission; one-shot
  consumed_at     timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now()
);

-- (10) Git projection identity + audit (ADR-03 §1 table 10, BUILD-C increment C6;
-- ADR-09 §4/§3). git_identity maps a verified git committer email to an ordinary
-- catalog principal + the scope its pushes bind at (ADR-07 step 2a), on the
-- agent_key pattern. An unmapped identity is rejected at scope-bind (no admission
-- row, a gate_refusal row only). Rotation is revoked=true.
CREATE TABLE IF NOT EXISTS git_identity (
  email       text PRIMARY KEY,        -- the verified git committer email
  actor_kind  text NOT NULL DEFAULT 'engineer',
  actor_id    text NOT NULL,
  scope_kind  smallint NOT NULL DEFAULT 0 CHECK (scope_kind BETWEEN 0 AND 4),
  scope_id    text NOT NULL DEFAULT '',
  revoked     bool NOT NULL DEFAULT false,
  created_at  timestamptz NOT NULL DEFAULT now()
);

-- projection_audit is the ADR-09 §3 self-heal event log: when a projection finds
-- the mirror's main SHA diverged from the computed head (force-push mangle), it
-- force-restores from the image and writes one row here. Append-only; the image is
-- truth, the mirror is a cache, and a divergence leaves this durable trace only.
CREATE TABLE IF NOT EXISTS projection_audit (
  id         bigserial PRIMARY KEY,
  event      text NOT NULL,           -- 'force-restore'
  detail     jsonb NOT NULL,          -- { mangled_sha, restored_sha, ref }
  created_at timestamptz NOT NULL DEFAULT now()
);

-- STAGE-A-PLAN pin #10 minimal epoch table (ADR-10/ADR-08).
CREATE TABLE IF NOT EXISTS epoch (
  n                    int PRIMARY KEY,
  std_manifest_root    text NOT NULL,
  dispatch_attestation text NOT NULL,
  created_at           timestamptz NOT NULL DEFAULT now()
);

-- ADR-08 §2: the fleet-coherence fence row — the single live catalog epoch.
-- Defined after epoch so its FK target exists.
CREATE TABLE IF NOT EXISTS epoch_current (
  one bool PRIMARY KEY DEFAULT true CHECK (one),
  n   int NOT NULL REFERENCES epoch(n)
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
    (name, scope_kind, scope_id, hash, visibility, valid_from, valid_to, admission_id)
  VALUES (NEW.name, NEW.scope_kind, NEW.scope_id, NEW.hash, NEW.visibility, ts, NULL,
          NEW.admission_id);
  RETURN NEW;
END; $$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS name_pointer_history_writer ON name_pointer;
CREATE TRIGGER name_pointer_history_writer
  BEFORE INSERT OR UPDATE ON name_pointer
  FOR EACH ROW EXECUTE FUNCTION regel_write_history();
