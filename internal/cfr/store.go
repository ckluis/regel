package cfr

import (
	"context"
	"errors"

	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/pgwire"
)

// DB is the transactional Postgres surface the store needs (ADR-05 §7).
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgwire.Result, error)
	Query(ctx context.Context, sql string, args ...any) (*pgwire.Rows, error)
	QueryRow(ctx context.Context, sql string, args []any, dest ...any) (bool, error)
	BeginSerializable(ctx context.Context) error
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

var (
	ErrCapabilityRefused = errors.New("cfr: restart capability refused")
	ErrRestartNotFound   = errors.New("cfr: restart not found")
	ErrNotResolved       = errors.New("cfr: continuation has no resolved restart")
	errNotImplemented    = errors.New("cfr: store not implemented")
)

// ParkReq is the input to Park (ADR-05 §6).
type ParkReq struct {
	State       *cek.State
	Kind        string
	RootDefHash string
	Class       string
	Payload     map[string]any
	Restarts    []cek.Restart
	Principal   map[string]any
}

// Park writes a parked continuation + durable condition + restart rows in one
// SERIALIZABLE transaction (ADR-05 §6/§7). RED: not yet implemented.
func Park(ctx context.Context, db DB, req ParkReq) (continuationID, conditionID string, err error) {
	return "", "", errNotImplemented
}

// PickRestart resolves a durable condition by choosing a restart (ADR-05 §6).
func PickRestart(ctx context.Context, db DB, conditionID, restartName string, args map[string]any, resolvedBy string, grantedCaps []string) error {
	return errNotImplemented
}

// ClaimAndResume performs the claim CAS + step transaction (ADR-05 §7).
func ClaimAndResume(ctx context.Context, db DB, continuationID string, seenSeq int64, kernelID string,
	resume func(state *cek.State, choice cek.RestartChoice) cek.Outcome) (cek.Outcome, bool, error) {
	return cek.Outcome{}, false, errNotImplemented
}
