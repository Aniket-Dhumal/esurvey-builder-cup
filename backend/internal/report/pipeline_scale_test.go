package report

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/platform/db"
)

// How the claim pipeline behaves when assessments fall behind.
//
// At a lakh of claims a day the assessment queue is the slowest stage -- it is
// bounded by the satellite and model quotas -- so claims pile up waiting on
// it. Each waiting claim is looked at again every minute. The question is
// whether that polling leaves the loop any time for new claims, whose farmers
// are waiting on the receipt.

type waitingAssessor struct{}

func (waitingAssessor) Queue(context.Context, db.Querier, string, string, string, string, time.Time) (string, error) {
	return "", nil
}
func (waitingAssessor) Ended(context.Context, string) (bool, error) { return false, nil }

func TestPipelineThroughputWithAnAssessmentBacklog(t *testing.T) {
	if os.Getenv("ESURVEY_SCALE") == "" {
		t.Skip("set ESURVEY_SCALE=1 to measure the pipeline under a backlog")
	}
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	postAuthorities(t, pool)
	ctx := context.Background()

	template := completeClaim(t, s, pool, "ps-template-0001")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM crop_loss_reports WHERE reference LIKE 'PS-%'`)
	})

	clone := func(prefix string, n int, state, assessment string, dueAgo string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO crop_loss_reports (
				reference, survey_event_id, farmer_id, farmer_name,
				village_lgd_code, village_name, sub_district_lgd_code,
				sub_district_name, district_lgd_code, district_name,
				season, crop_year, loss_category, calamity_type, occurred_at,
				reported_channel, declaration_accepted, documents_promised,
				farmer_signature_kind, farmer_signed_at, state, client_ref,
				assessment_state, evidence_complete_at, pipeline_next_at)
			SELECT $1 || g, r.survey_event_id, r.farmer_id, r.farmer_name,
			       r.village_lgd_code, r.village_name, r.sub_district_lgd_code,
			       r.sub_district_name, r.district_lgd_code, r.district_name,
			       r.season, r.crop_year, r.loss_category, r.calamity_type,
			       r.occurred_at, r.reported_channel, r.declaration_accepted,
			       r.documents_promised, r.farmer_signature_kind,
			       r.farmer_signed_at, $3::text, $1 || 'c' || g,
			       $4, now(), now() - $5::interval
			  FROM generate_series(1, $2) g,
			       (SELECT * FROM crop_loss_reports WHERE id = $6) r`,
			prefix, n, state, assessment, dueAgo, template); err != nil {
			t.Fatalf("seeding %s: %v", prefix, err)
		}
	}

	measure := func(label string, loops int, waiting int) {
		_, _ = pool.Exec(ctx, `DELETE FROM crop_loss_reports WHERE reference LIKE 'PS-%'`)
		// The backlog: claims acknowledged and waiting on an assessment, all
		// due for their next look -- they were queued before the new claims.
		if waiting > 0 {
			clone("PS-WAIT-", waiting, "acknowledged", "queued", "2 seconds")
		}
		const fresh = 200
		clone("PS-NEW-", fresh, "submitted", "none", "0 seconds")
		_, _ = pool.Exec(ctx, `ANALYZE crop_loss_reports`)

		var steps atomic.Int64
		deadline := time.Now().Add(45 * time.Second)
		begin := time.Now()
		var receipts time.Duration
		var wg sync.WaitGroup
		stop := make(chan struct{})
		for i := 0; i < loops; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for time.Now().Before(deadline) {
					select {
					case <-stop:
						return
					default:
					}
					_, worked, err := s.AdvanceOne(ctx, waitingAssessor{})
					if err != nil {
						continue
					}
					if !worked {
						time.Sleep(50 * time.Millisecond)
						continue
					}
					steps.Add(1)
				}
			}()
		}
		for time.Now().Before(deadline) {
			var left int
			_ = pool.QueryRow(ctx, `SELECT count(*) FROM crop_loss_reports
				WHERE reference LIKE 'PS-NEW-%' AND state = 'submitted'`).Scan(&left)
			if left == 0 {
				receipts = time.Since(begin)
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		close(stop)
		wg.Wait()
		elapsed := time.Since(begin)
		rate := float64(steps.Load()) / elapsed.Seconds()
		if receipts == 0 {
			t.Logf("%s: %d loop(s), %d waiting: %.0f steps/s; %d new claims NOT all acknowledged within 45s",
				label, loops, waiting, rate, fresh)
		} else {
			t.Logf("%s: %d loop(s), %d waiting: %.0f steps/s; %d new claims acknowledged in %s",
				label, loops, waiting, rate, fresh, receipts.Round(time.Millisecond))
		}
	}

	measure("no backlog", 1, 0)
	measure("backlog", 1, 20000)
	measure("backlog", 8, 20000)
}
