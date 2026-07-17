// stranger_review.go — the R1-14 / ARCHITECTURE-M6 stranger-review gate,
// mechanically: the review having happened and its verdict being recorded IS the
// gate. A missing row, or a latest verdict that is not 'finished', reads RED —
// exactly like an un-run suite. Nothing here judges the dashboard; it judges
// whether an OUTSIDE reviewer's judgment exists as a row.
package admission

import (
	"context"

	"regel.dev/regel/internal/pgwire"
)

// StrangerReview is the recorded gate entry for one target.
type StrangerReview struct {
	Reviewer string
	Verdict  string // 'finished' | 'unfinished'
	Notes    string
}

// RecordStrangerReview inserts the outside reviewer's verdict for a target.
func RecordStrangerReview(ctx context.Context, conn *pgwire.Conn, target string, r StrangerReview) error {
	_, err := conn.Exec(ctx, `
INSERT INTO stranger_review (target, reviewer, verdict, notes) VALUES ($1, $2, $3, $4)`,
		target, r.Reviewer, r.Verdict, r.Notes)
	return err
}

// StrangerReviewGate reads the gate for a target: green ⇔ the LATEST recorded
// review exists AND its verdict is 'finished'. (found=false, green=false) is the
// un-run-suite RED; an 'unfinished' latest verdict is an explicit RED.
func StrangerReviewGate(ctx context.Context, conn *pgwire.Conn, target string) (green, found bool, latest StrangerReview, err error) {
	found, err = conn.QueryRow(ctx, `
SELECT reviewer, verdict, notes FROM stranger_review
WHERE target = $1 ORDER BY reviewed_at DESC, id DESC LIMIT 1`,
		[]any{target}, &latest.Reviewer, &latest.Verdict, &latest.Notes)
	if err != nil || !found {
		return false, found, latest, err
	}
	return latest.Verdict == "finished", true, latest, nil
}
