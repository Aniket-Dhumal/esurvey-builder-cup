package report

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5/pgtype"
	"time"

	"github.com/jackc/pgx/v5"
)

// Keeping the rollups current.
//
// # What this is for
//
// Every aggregate in the platform is computed live, which is correct at
// today's volume and cannot be at the planning estimate of order 10^6 claims
// a season. A State Officer's dashboard is a full scan of the largest table in
// the database, for the officer with the whole state in scope.
//
// So one row per (event, village, crop) is maintained here, and every level
// above the village is summed from it at query time. See migration 0030 for
// why the leaf is the village and why percentages are stored as numerators.
//
// # Why the sweep is incremental and bounded
//
// A trigger records which (event, village) pairs a write touched. This drains
// that queue in batches, recomputing only what changed. During an event that
// is a handful of villages a minute; at rest it is nothing, and the ticker
// costs one indexed query that returns no rows.
//
// Bounded because the alternative is a sweep that is still running when the
// next one starts. A batch that does not finish the queue leaves the rest
// marked, and the next tick takes it.

// RollupBatch is how many (event, village) pairs one sweep recomputes.
//
// Each is a small aggregate over one village's claims for one event -- tens of
// rows, hundreds in a bad monsoon. Two hundred of them is a sweep measured in
// milliseconds, which is the point: the work has to fit comfortably between
// ticks even when a district has just filed all day.
const RollupBatch = 200

// RollupRows is how many queue rows one sweep looks at to find those pairs.
//
// The queue is append-only, so a village where twenty farmers filed in a
// minute is twenty rows and one piece of work; a district-wide event is a
// lakh of rows over a handful of pairs. Looking at many rows per sweep is what
// keeps the queue draining faster than a district can dirty it -- the rows are
// three narrow columns, and all but the ones this sweep recomputes are left
// alone.
const RollupRows = 20000

// RefreshRollups recomputes what has changed. Returns how many keys it did.
//
// Claimed with FOR UPDATE SKIP LOCKED so two API instances sweeping the same
// queue divide the work rather than doing it twice.
func (s *Store) RefreshRollups(ctx context.Context) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	// The queue is append-only, so one pair can be marked many times. Rows are
	// claimed by identity -- and deleted by identity below -- for two reasons:
	// writers never wait on a unique key (see migration 0034), and a mark made
	// while this sweep is recomputing survives it. Deleting by (event, village)
	// used to remove such a mark, leaving the rollup quietly stale until the
	// next report in that village.
	rows, err := tx.Query(ctx, `
		SELECT ctid, survey_event_id, village_lgd_code
		  FROM report_rollup_dirty
		 ORDER BY marked_at
		 LIMIT $1
		 FOR UPDATE SKIP LOCKED`, RollupRows)
	if err != nil {
		return 0, fmt.Errorf("claiming rollup work: %w", err)
	}
	type key struct{ event, village string }
	type marked struct {
		id pgtype.TID
		k  key
	}
	var rowsRead []marked
	seen := map[key]bool{}
	var keys []key
	for rows.Next() {
		var m marked
		if err := rows.Scan(&m.id, &m.k.event, &m.k.village); err != nil {
			rows.Close()
			return 0, err
		}
		if !seen[m.k] && len(keys) < RollupBatch {
			seen[m.k] = true
			keys = append(keys, m.k)
		}
		rowsRead = append(rowsRead, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(keys) == 0 {
		return 0, nil
	}

	for _, k := range keys {
		if err := refreshOne(ctx, tx, k.event, k.village); err != nil {
			return 0, fmt.Errorf("rolling up %s in %s: %w", k.event, k.village, err)
		}
	}

	// Every claimed row whose pair has just been recomputed goes -- twenty
	// marks for one village are one piece of work, and leaving nineteen behind
	// would have the queue growing faster than it drains. Rows for pairs this
	// sweep did not reach stay marked and stay claimed by nobody, so the next
	// sweep takes them.
	claimed := make([]pgtype.TID, 0, len(rowsRead))
	for _, m := range rowsRead {
		if seen[m.k] {
			claimed = append(claimed, m.id)
		}
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM report_rollup_dirty WHERE ctid = ANY($1)`, claimed); err != nil {
		return 0, err
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(keys), nil
}

// refreshOne rebuilds every crop row for one event in one village.
//
// Delete-then-insert rather than upsert, because a crop can leave: a report
// whose only sugarcane line was corrected to sorghum must not leave a
// sugarcane row behind claiming an area nobody now reports.
func refreshOne(ctx context.Context, tx pgx.Tx, eventID, village string) error {
	if _, err := tx.Exec(ctx, `
		DELETE FROM report_rollups
		 WHERE survey_event_id = $1 AND village_lgd_code = $2`,
		eventID, village); err != nil {
		return err
	}

	_, err := tx.Exec(ctx, `
		INSERT INTO report_rollups (
			survey_event_id, village_lgd_code, crop_name,
			reports, farmers, area_sown_ha, area_affected_ha,
			unresolved, adjudicated, contested, filed_late,
			declared_pct_area, adjudicated_pct_area, adjudicated_area_ha,
			refreshed_at)
		SELECT r.survey_event_id, r.village_lgd_code,
		       COALESCE(i.crop_name, ''),

		       count(DISTINCT r.id),
		       count(DISTINCT r.farmer_id),
		       COALESCE(sum(i.area_sown_ha), 0),
		       COALESCE(sum(i.area_affected_ha), 0),

		       count(DISTINCT r.id) FILTER (
		           WHERE r.state NOT IN ('adjudicated', 'closed', 'withdrawn')),
		       count(DISTINCT r.id) FILTER (WHERE r.adjudicated_percent IS NOT NULL),
		       count(DISTINCT r.id) FILTER (WHERE r.contested),
		       count(DISTINCT r.id) FILTER (WHERE r.filed_late),

		       -- Numerators, weighted by the area each figure applies to.
		       COALESCE(sum(i.damage_percent * i.area_affected_ha), 0),
		       COALESCE(sum(r.adjudicated_percent * i.area_affected_ha)
		                FILTER (WHERE r.adjudicated_percent IS NOT NULL), 0),
		       COALESCE(sum(i.area_affected_ha)
		                FILTER (WHERE r.adjudicated_percent IS NOT NULL), 0),

		       now()
		  FROM crop_loss_reports r
		  -- LEFT, so a report with no crop lines still counts as a report.
		  -- An inner join drops it, and a claim that exists and is counted
		  -- nowhere is the one an oversight officer will be asked about.
		  LEFT JOIN crop_loss_report_items i ON i.report_id = r.id
		 WHERE r.survey_event_id = $1 AND r.village_lgd_code = $2
		 GROUP BY r.survey_event_id, r.village_lgd_code, COALESCE(i.crop_name, '')`,
		eventID, village)
	return err
}

// RunRollups refreshes on a ticker until the context ends.
func (s *Store) RunRollups(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// Drain rather than do one batch: a district that filed all
			// morning should be current by the next tick, not five ticks
			// later. Bounded by the loop below rather than by the batch, so a
			// backlog cannot pin the sweeper for ever.
			for i := 0; i < 20; i++ {
				n, err := s.RefreshRollups(ctx)
				if err != nil {
					// Logged and dropped. The queue keeps the marks, so the
					// next tick tries again; a sweeper that returned here
					// would leave the rollups frozen at whatever they were
					// when the database hiccupped, with no sign on any
					// screen that reads them.
					if s.log != nil {
						s.log.Error("refreshing rollups", "error", err)
					}
					break
				}
				if n < RollupBatch {
					break
				}
			}
		}
	}
}
