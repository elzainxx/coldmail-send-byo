// account_score.go — Phase 5 Chunk 3: the account status-score model (§3). The score is a
// deterministic, pure function of send-viability signals ONLY (consecutive_failures, daily
// proximity, time since last success — §3.1); it uses NO bounce data (§3.3 carve-out). It is a
// STORED SNAPSHOT recomputed at OUTCOME time on the one account that just had an outcome
// (never a table sweep), and it is INDEPENDENT of the paused/quarantined flags — the flags never
// reset or freeze it; a quarantined account gets no outcomes so it simply HOLDS its score (§3.3).
//
// Chunk 2b currently wires the failure BOOKKEEPING (consecutive_failures += 1, last_failure_at =
// NOW()) and returns the account's stored score. Chunk 3 replaces the returned value with the real
// §3.2 weighted-penalty formula computed from the just-updated row + status_score_weights, and
// writes it back to account_status_score. The seam (recomputeScoreOnFailure / recomputeScoreOnSuccess)
// is stable so Chunk 3 fills the formula without touching account_effect.go / outcome.go.
package dispatch

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// recomputeScoreOnFailure applies a penalizing failure's account bookkeeping (§3.3): increment
// consecutive_failures, stamp last_failure_at=NOW(), then recompute + store the score and RETURN
// the new value (so the quarantine decision can compare it to the threshold in the same tx).
//
// CHUNK 2b: does the bookkeeping and returns the current stored score (the flag policy for
// auth_failure/throttle is exercised independent of the exact number). CHUNK 3 will compute the
// real §3.2 score from the updated row + weights and store it. Marked so the split is honest — the
// score-below-threshold quarantine trigger (§4.2b) is only fully live once Chunk 3 lands the formula.
func (h *OutcomeHandler) recomputeScoreOnFailure(ctx context.Context, tx pgx.Tx, accountID string) (int, error) {
	var score int
	err := tx.QueryRow(ctx,
		`UPDATE sender.accounts
		   SET consecutive_failures = consecutive_failures + 1,
		       last_failure_at = NOW(),
		       updated_at = NOW()
		 WHERE id = $1
		 RETURNING account_status_score`, accountID).Scan(&score)
	return score, err
}
