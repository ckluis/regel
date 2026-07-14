package admission

// Verdict is the Stage-A subset of the ADR-07 §6 structured Verdict (STAGE-A-PLAN
// pin #6). One schema for humans and agents; JSON-marshalable and returned
// identically by the HTTP door and the CLI door. `delta` and `seeders` are
// deferred to Stage C (agent plane) — named residue.
type Verdict struct {
	// Outcome is the typed discriminant every door switches on (ADR-07 R1-08).
	Outcome string `json:"outcome"`
	// Admitted == (Outcome == OutcomeAdmitted); retained for convenience.
	Admitted bool `json:"admitted"`
	// Hashes maps each target catalog name to its computed content address —
	// computed even on reject when lowering succeeded (identity is free).
	Hashes map[string]string `json:"hashes"`
	// Stages records each pipeline stage's status and wall time.
	Stages []Stage `json:"stages"`
	// Diagnostics are the machine-actionable refusals (empty on green).
	Diagnostics []Diagnostic `json:"diagnostics"`
	// RefusalID is the durable gate_refusal PK, minted before returning on every
	// non-green outcome; "" on admitted/already-admitted.
	RefusalID string `json:"refusal_id,omitempty"`
	// AdmissionID is set iff the admission committed (admitted / already-admitted).
	AdmissionID int64 `json:"admission_id,omitempty"`
	// Epoch + BaseSnapshot pin the frozen snapshot the verdict was computed over.
	Epoch        int    `json:"epoch"`
	BaseSnapshot string `json:"base_snapshot"`
	// Delta is the machine-computed blast-radius delta (ADR-07 §6, R1-04),
	// attached on EVERY run — green or red. A pure projection of what V1/V2/V6
	// computed over the frozen snapshot vs. the base.
	Delta Delta `json:"delta"`
	// Seeders is the content-seeder set (ADR-07 §1 / ADR-12 §6): the provenance of
	// the rows the authoring session read that reach this patch. Empty for
	// human/CLI submissions. Bound from the authenticated principal's scope chain
	// at step 2a (an out-of-chain seeder is unrepresentable, so rejected).
	Seeders []Seeder `json:"seeders"`
}

// Delta is the blast-radius delta (ADR-07 §6). Each *_added_vs_base names only
// the surface this patch widens relative to the base snapshot.
type Delta struct {
	Capabilities CapDelta `json:"capabilities"`
	PIISurface   PIIDelta `json:"pii_surface"`
	DDLSurface   DDLDelta `json:"ddl_surface"`
}

// CapDelta is the capability delta from V1 capability-audit.
type CapDelta struct {
	Requested   []string `json:"requested"`
	Granted     []string `json:"granted"`
	AddedVsBase []string `json:"added_vs_base"`
}

// PIIDelta is the pii-surface delta from V2 pii-flow: fields/values reaching a
// boundary sink, and those newly reaching one vs. the base.
type PIIDelta struct {
	Touched     []string `json:"touched"`
	AddedVsBase []string `json:"added_vs_base"`
}

// DDLDelta is the DDL-surface delta from V6 derivation-parity.
type DDLDelta struct {
	Statements  []string `json:"statements"`
	Additive    bool     `json:"additive"`
	AddedVsBase []string `json:"added_vs_base"`
}

// Seeder is one content-seeder provenance record (ADR-07 §1 / ADR-12 §6).
type Seeder struct {
	SourceKind string `json:"source_kind"`
	SourceRef  string `json:"source_ref"`
	Scope      Scope  `json:"scope"`
	SeededBy   string `json:"seeded_by"` // "unattributed" for an external-effect source
}

// Outcome constants — the full ADR-07 §6 seven-value enum (as string constants).
const (
	OutcomeAdmitted        = "admitted"
	OutcomeAlreadyAdmitted = "already-admitted"
	OutcomeRejected        = "rejected"
	OutcomeStaleBase       = "stale-base"
	OutcomeRetryExhausted  = "retry-exhausted"
	OutcomeBudgetExhausted = "budget-exhausted" // pre-BEGIN; not reachable in Stage A
	OutcomeBusy            = "busy"             // pre-BEGIN; not reachable in Stage A
)

// Stage is one pipeline stage's outcome for the Verdict timeline.
type Stage struct {
	Stage  string `json:"stage"`
	Status string `json:"status"` // pass | fail | skip
	Ms     int64  `json:"ms"`
}

// Loc anchors a diagnostic to a definition and a source span.
type Loc struct {
	DefHash string `json:"def_hash,omitempty"`
	Span    string `json:"span,omitempty"`
}

// Diagnostic is one machine-actionable refusal (ADR-07 §6 shape).
type Diagnostic struct {
	StageOrVerifier string `json:"stage_or_verifier"`
	Code            string `json:"code"`
	Severity        string `json:"severity"`
	Subject         string `json:"subject,omitempty"`
	Loc             Loc    `json:"loc"`
	Message         string `json:"message"`
	Fix             string `json:"fix,omitempty"`
}

// nonGreen reports whether an outcome mints a durable refusal_id.
func nonGreen(outcome string) bool {
	return outcome != OutcomeAdmitted && outcome != OutcomeAlreadyAdmitted
}

// httpStatus maps an outcome to its Stage-A HTTP status (STAGE-A-PLAN pin #5).
func HTTPStatus(outcome string) int {
	switch outcome {
	case OutcomeAdmitted, OutcomeAlreadyAdmitted:
		return 200
	case OutcomeStaleBase:
		return 409
	case OutcomeRejected:
		return 422
	case OutcomeBusy, OutcomeBudgetExhausted:
		return 429
	default:
		return 500
	}
}
