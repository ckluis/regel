package admission

import "testing"

// The R1-14 gate red-path: an ABSENT review reads RED (like an un-run suite), an
// 'unfinished' latest verdict reads RED, and only a recorded 'finished' verdict
// reads GREEN. The gate is the row's existence — never a default.
func TestStrangerReviewGateReadsRedWhenAbsent(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)

	// RED: no review recorded.
	green, found, _, err := StrangerReviewGate(ctx, w.conn, "reference-dashboard")
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if found || green {
		t.Fatalf("absent review must read RED/not-found; got green=%v found=%v", green, found)
	}

	// RED: an explicit 'unfinished' verdict.
	if err := RecordStrangerReview(ctx, w.conn, "reference-dashboard",
		StrangerReview{Reviewer: "llm-stranger:test", Verdict: "unfinished", Notes: "cards lack titles"}); err != nil {
		t.Fatalf("record: %v", err)
	}
	green, found, latest, err := StrangerReviewGate(ctx, w.conn, "reference-dashboard")
	if err != nil || !found {
		t.Fatalf("gate after unfinished: green=%v found=%v err=%v", green, found, err)
	}
	if green {
		t.Fatalf("unfinished verdict must read RED; got green with %+v", latest)
	}

	// GREEN: a later 'finished' verdict from the outside reviewer.
	if err := RecordStrangerReview(ctx, w.conn, "reference-dashboard",
		StrangerReview{Reviewer: "llm-stranger:test", Verdict: "finished", Notes: "looks done"}); err != nil {
		t.Fatalf("record: %v", err)
	}
	green, found, latest, err = StrangerReviewGate(ctx, w.conn, "reference-dashboard")
	if err != nil || !found || !green {
		t.Fatalf("finished verdict must read GREEN; green=%v found=%v latest=%+v err=%v", green, found, latest, err)
	}
	if latest.Reviewer != "llm-stranger:test" {
		t.Fatalf("latest reviewer = %q", latest.Reviewer)
	}
}
