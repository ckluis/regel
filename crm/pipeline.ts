import { connect, query } from "std/sql";
import type { Row } from "std/sql";

// activePipeline — a typed std/sql read (ADR-10 §4 D1): SELECT-only and
// $1-parameterized over the DERIVED Account table (no string SQL is interpolable —
// that is the read-safety guarantee). sql.query is capability-gated (`sql.query`)
// and effect class `read` (inline, no checkpoint), and it honors the eval's as-of
// read context. This is the app-side counterpart to the derived dashboard's
// aggregates: the CRM reads its own governed rows through the same door any
// admitted code does.
export function activePipeline(): Row[] {
  const c = connect();
  return query(
    c,
    "SELECT name, arr FROM res_app_crm_account WHERE stage = $1 ORDER BY name",
    ["active"],
  );
}
