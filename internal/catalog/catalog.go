// Package catalog is the regel governed substrate: embedded DDL, idempotent
// bootstrap, and the single name resolver (live + as-of). It runs entirely over
// the owned internal/pgwire client.
package catalog

import (
	"context"
	_ "embed"
	"fmt"
	"regexp"

	"regel.dev/regel/internal/pgwire"
)

//go:embed sql/schema.sql
var schemaSQL string

// schemaVersion is the applied DDL revision recorded in schema_version.
const schemaVersion = 1

var identRE = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// Bootstrap applies the substrate DDL idempotently over adminConn (a superuser /
// table-owner connection, e.g. clank). When kernelRole is non-empty it creates
// that NOLOGIN-free runtime role and applies the ADR-03 I6 privilege posture:
// SELECT+INSERT on the immortal store, but UPDATE/DELETE revoked, so a runtime
// kernel connecting as kernelRole cannot mutate or delete a definition row.
func Bootstrap(ctx context.Context, adminConn *pgwire.Conn, kernelRole string) error {
	if kernelRole != "" && !identRE.MatchString(kernelRole) {
		return fmt.Errorf("catalog: invalid kernel role name %q", kernelRole)
	}
	if kernelRole != "" {
		createRole := fmt.Sprintf(`DO $$ BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '%s') THEN
    CREATE ROLE %s LOGIN;
  END IF;
END $$;`, kernelRole, kernelRole)
		if _, err := adminConn.ExecSimple(ctx, createRole); err != nil {
			return fmt.Errorf("catalog: create kernel role: %w", err)
		}
	}

	if _, err := adminConn.ExecSimple(ctx, schemaSQL); err != nil {
		return fmt.Errorf("catalog: apply schema: %w", err)
	}

	if kernelRole != "" {
		if _, err := adminConn.ExecSimple(ctx, kernelGrants(kernelRole)); err != nil {
			return fmt.Errorf("catalog: apply kernel grants: %w", err)
		}
	}

	if _, err := adminConn.Exec(ctx,
		"INSERT INTO schema_version(version) VALUES ($1) ON CONFLICT DO NOTHING",
		schemaVersion); err != nil {
		return fmt.Errorf("catalog: record schema version: %w", err)
	}
	return nil
}

// kernelGrants builds the runtime-role privilege posture. The immortal store
// (definition, definition_meta) is SELECT+INSERT only; name_pointer_history is
// SELECT only (the SECURITY DEFINER I7 trigger, owned by the bootstrap user,
// writes it — application code never does); everything else is read/write.
func kernelGrants(role string) string {
	return fmt.Sprintf(`
-- Immortal store: insert + read, never mutate or delete (ADR-03 I6).
GRANT SELECT, INSERT ON definition, definition_meta TO %[1]s;
REVOKE UPDATE, DELETE ON definition, definition_meta FROM PUBLIC;
REVOKE UPDATE, DELETE ON definition, definition_meta FROM %[1]s;
-- History: read-only for the kernel; the SECURITY DEFINER trigger writes it.
GRANT SELECT ON name_pointer_history TO %[1]s;
REVOKE INSERT, UPDATE, DELETE ON name_pointer_history FROM PUBLIC;
REVOKE INSERT, UPDATE, DELETE ON name_pointer_history FROM %[1]s;
-- Mutable + ledger tables.
GRANT SELECT, INSERT, UPDATE, DELETE ON
  name_pointer, admission, gate_refusal, continuation, durable_condition,
  restart, task, grant_row, verifier_coverage, perf_budget,
  continuation_coverage, epoch, epoch_current, std_manifest,
  migration_finding, epoch_hold, schema_version,
  derived_resource, derived_artifact, vault, vault_key, shred_attestation,
  admission_fuel, admission_capacity, agent_key, approval_token TO %[1]s;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO %[1]s;
`, role)
}
