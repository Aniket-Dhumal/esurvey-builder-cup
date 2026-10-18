// Package notify tells the farmer.
//
// # The gap this closes
//
// The platform decided things that mattered to a farmer -- their district
// opened a survey event, their report was acknowledged, an officer was
// coming, their appeal was answered -- and told them none of it. A farmer had
// to already know to open the app. That is not a missing feature at the edge;
// it is most of the adoption problem.
//
// # Why the messages are written even with no gateway
//
// No SMS provider is connected yet. The honest behaviour is to queue and
// report the backlog rather than drop messages and look healthy: when a
// provider is connected everything queued is still there, and until then the
// count is the true measure of how much this platform is failing to tell
// people. A no-op sender that reported success would hide exactly that.
package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/platform/db"
)

// Event codes. Strings, because they are stored and outlive any enum.
const (
	EventReportAcknowledged = "report.acknowledged"
	EventReportVerified     = "report.verified"
	EventReportAdjudicated  = "report.adjudicated"
	EventSurveyOpened       = "survey.opened"
	EventSurveyClosing      = "survey.closing"
	EventGrievanceFiled     = "grievance.filed"
	EventGrievanceDecided   = "grievance.decided"
)

// MaxAttempts before a message is abandoned for a person to look at.
//
// Five, with backoff, spans roughly a day. A handset that is off for a day is
// ordinary; one that is unreachable for longer is a wrong number, and
// retrying it forever costs money and tells nobody.
const MaxAttempts = 5

// Message is one thing to tell one person.
type Message struct {
	EventCode    string
	FarmerID     string
	Recipient    string
	Channel      string
	Language     string
	Body         string
	ResourceType string
	ResourceID   string
}

// Sender delivers a message. Implemented by a real gateway in deployment.
type Sender interface {
	// Send returns the provider's reference for the message, or an error.
	Send(ctx context.Context, m Message) (ref string, err error)
	// Name identifies the provider in the record.
	Name() string
}

// ErrNoSender is returned when nothing is configured to deliver.
//
// Distinct from a delivery failure, because it is not one: the message is
// fine, the platform simply has no way to send it, and treating that as a
// failed attempt would burn the retry budget on a problem no retry fixes.
var ErrNoSender = errors.New("no notification provider is configured")

// DefaultWorkers is how many messages are in flight at once.
//
// # Why more than one
//
// Sending was a loop: claim a hundred, send them one after another, wait for
// the next tick. A gateway answering in eighty milliseconds -- a fast one --
// meant twelve messages a second, so one district-wide event's
// acknowledgements took hours and the decisions weeks later took hours again
// (docs/SYNC_SCALE_REVIEW.md). Nothing about the work is serial: each message
// is an independent HTTPS call.
//
// # Why not more
//
// The ceiling is the aggregator's, not ours. Indian SMS aggregators sell
// throughput in messages per second and refuse above it, and a refusal here
// costs a retry and a farmer's notice arriving later. Sixteen at eighty
// milliseconds is two hundred a second, which is at or above what a DLT-
// registered sender is usually given; raise it with the contract, through
// ESURVEY_SMS_WORKERS, not by editing this.
const DefaultWorkers = 16

// Queue writes and delivers notifications.
type Queue struct {
	pool   *db.Pool
	sender Sender
	log    *slog.Logger

	// workers bounds how many sends are in flight.
	workers int
}

func NewQueue(pool *db.Pool, sender Sender, log *slog.Logger) *Queue {
	return &Queue{pool: pool, sender: sender, log: log, workers: DefaultWorkers}
}

// WithWorkers sets how many messages may be in flight at once. Zero or less
// leaves the default.
func (q *Queue) WithWorkers(n int) *Queue {
	if n > 0 {
		q.workers = n
	}
	return q
}

// Enqueue records a message to be sent.
//
// Takes a transaction where the caller has one: the whole point of an outbox
// is that the intent to notify commits with the thing that caused it. A
// report that is acknowledged and a message that was never queued is the
// failure this avoids.
func (q *Queue) Enqueue(ctx context.Context, tx db.Querier, m Message) error {
	if strings.TrimSpace(m.Recipient) == "" {
		// Not an error. Plenty of farmers have no number on record, and
		// refusing the surrounding transaction over it would block the
		// acknowledgement itself.
		q.log.Debug("no recipient for notification",
			"event", m.EventCode, "farmer", m.FarmerID)
		return nil
	}
	if strings.TrimSpace(m.Body) == "" {
		return fmt.Errorf("notification %s has no body", m.EventCode)
	}
	if m.Channel == "" {
		m.Channel = "sms"
	}
	if m.Language == "" {
		m.Language = "mr"
	}

	if tx == nil {
		tx = q.pool
	}

	// Opting out is checked at enqueue rather than at send, so a suppressed
	// message is visible as a decision rather than as an absence.
	var optedOut bool
	if m.FarmerID != "" {
		_ = tx.QueryRow(ctx, `
			SELECT opted_out_at IS NOT NULL FROM notification_preferences
			WHERE farmer_id = $1`, m.FarmerID).Scan(&optedOut)
	}

	state, reason := "queued", any(nil)
	if optedOut {
		state, reason = "suppressed", "the farmer has opted out of messages"
	}

	_, err := tx.Exec(ctx, `
		INSERT INTO notifications (
			event_code, farmer_id, recipient, channel, language, body,
			resource_type, resource_id, state, suppressed_reason
		) VALUES (
			$1, NULLIF($2, ''), $3, $4::notification_channel, $5, $6,
			NULLIF($7, ''), NULLIF($8, ''), $9::notification_state, $10
		)`,
		m.EventCode, m.FarmerID, m.Recipient, m.Channel, m.Language, m.Body,
		m.ResourceType, m.ResourceID, state, reason)
	if err != nil {
		return fmt.Errorf("queueing notification: %w", err)
	}
	return nil
}

// Backlog is what is waiting, for the readiness endpoint.
type Backlog struct {
	Queued    int    `json:"queued"`
	Failed    int    `json:"failed"`
	Abandoned int    `json:"abandoned"`
	Sent      int    `json:"sent"`
	Oldest    string `json:"oldest_queued,omitempty"`
	Provider  string `json:"provider"`

	// LastError is the most recent delivery failure, in the gateway's own
	// words. What an operator needs is not that sending is failing but why:
	// a zero balance, an unregistered header and a network outage are three
	// different jobs, and only one of them fixes itself.
	LastError string `json:"last_error,omitempty"`
}

// Backlog counts what has not gone out.
func (q *Queue) Backlog(ctx context.Context) (*Backlog, error) {
	b := &Backlog{Provider: "none"}
	if q.sender != nil {
		b.Provider = q.sender.Name()
	}

	err := q.pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE state = 'queued'),
			count(*) FILTER (WHERE state = 'failed'),
			count(*) FILTER (WHERE state = 'abandoned'),
			count(*) FILTER (WHERE state = 'sent'),
			COALESCE(to_char(min(created_at) FILTER (WHERE state = 'queued'),
			                 'YYYY-MM-DD"T"HH24:MI:SSZ'), ''),
			-- Why the last one failed. A count of failures tells an operator
			-- that something is wrong and not one thing about what; the
			-- gateway's own words ("Insufficient Balance!") are what they
			-- need, and they are already stored.
			COALESCE((SELECT last_error FROM notifications
			           WHERE last_error IS NOT NULL
			           ORDER BY updated_at DESC LIMIT 1), '')
		FROM notifications`).
		Scan(&b.Queued, &b.Failed, &b.Abandoned, &b.Sent, &b.Oldest, &b.LastError)
	if err != nil {
		return nil, fmt.Errorf("reading the notification backlog: %w", err)
	}
	return b, nil
}

// claim is one message this dispatcher has taken responsibility for.
type claim struct {
	id       string
	m        Message
	attempts int
}

// Dispatch attempts everything due, up to limit.
//
// Returns how many were sent. A run with no sender configured is not an
// error and not a no-op: it reports how many are waiting, which is the
// number worth knowing.
func (q *Queue) Dispatch(ctx context.Context, limit int) (sent int, err error) {
	if q.sender == nil {
		return 0, ErrNoSender
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	// Claimed with a row lock and SKIP LOCKED, so two dispatchers -- or two
	// replicas of the same one -- never send the same message twice. Sending
	// a farmer the same notice twice is a small thing; doing it because the
	// deployment scaled is not.
	rows, err := q.pool.Query(ctx, `
		-- MATERIALIZED so the pick runs once and LIMIT is a limit: a bare
		-- IN (subquery) may be re-run, each run skipping the rows the last
		-- one locked.
		WITH picked AS MATERIALIZED (
			SELECT id FROM notifications
			WHERE state IN ('queued', 'failed') AND next_attempt_at <= now()
			ORDER BY next_attempt_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE notifications SET state = 'sending', attempts = attempts + 1
		WHERE id IN (SELECT id FROM picked)
		RETURNING id, event_code, COALESCE(farmer_id, ''), recipient,
		          channel::text, language, body,
		          COALESCE(resource_type, ''), COALESCE(resource_id, ''), attempts`,
		limit)
	if err != nil {
		return 0, fmt.Errorf("claiming notifications: %w", err)
	}

	var claims []claim
	for rows.Next() {
		var c claim
		if err := rows.Scan(&c.id, &c.m.EventCode, &c.m.FarmerID, &c.m.Recipient,
			&c.m.Channel, &c.m.Language, &c.m.Body,
			&c.m.ResourceType, &c.m.ResourceID, &c.attempts); err != nil {
			rows.Close()
			return 0, err
		}
		claims = append(claims, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	// Sent in parallel, bounded. The claim above already belongs to this
	// dispatcher -- another replica's SKIP LOCKED passed over it -- so the
	// only thing being shared here is the gateway.
	workers := q.workers
	if workers <= 0 {
		workers = DefaultWorkers
	}
	if workers > len(claims) {
		workers = len(claims)
	}

	var delivered atomic.Int64
	work := make(chan claim)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range work {
				if q.deliver(ctx, c) {
					delivered.Add(1)
				}
			}
		}()
	}
	for _, c := range claims {
		select {
		case <-ctx.Done():
			close(work)
			wg.Wait()
			return int(delivered.Load()), ctx.Err()
		case work <- c:
		}
	}
	close(work)
	wg.Wait()

	return int(delivered.Load()), nil
}

// deliver sends one message and records what happened. Reports success.
func (q *Queue) deliver(ctx context.Context, c claim) bool {
	ref, sendErr := q.sender.Send(ctx, c.m)
	if sendErr == nil {
		if _, err := q.pool.Exec(ctx, `
			UPDATE notifications
			SET state = 'sent', sent_at = now(), provider = $2,
			    provider_ref = $3, last_error = NULL
			WHERE id = $1`, c.id, q.sender.Name(), ref); err != nil {
			q.log.Error("recording a sent notification", "id", c.id, "error", err)
		}
		return true
	}

	// Abandoned rather than retried forever. A number that has refused
	// five times over a day is wrong, and the row stays for a person to
	// look at rather than disappearing.
	state := "failed"
	if c.attempts >= MaxAttempts {
		state = "abandoned"
		q.log.Warn("abandoning a notification",
			"id", c.id, "event", c.m.EventCode, "attempts", c.attempts)
	}

	// Exponential backoff in the column, not in a sleep: a restart must
	// not lose the schedule or retry everything at once.
	backoff := time.Duration(1<<uint(min(c.attempts, 6))) * time.Minute
	if _, err := q.pool.Exec(ctx, `
		UPDATE notifications
		SET state = $2::notification_state, last_error = $3,
		    next_attempt_at = now() + $4::interval
		WHERE id = $1`, c.id, state, sendErr.Error(), backoff.String()); err != nil {
		q.log.Error("recording a failed notification", "id", c.id, "error", err)
	}
	return false
}

// dispatchBatch is how many messages one round claims.
const dispatchBatch = 100

// SweepSent removes delivered notices older than keep, in bounded batches.
//
// # Why they go at all
//
// Two rows per claim -- the acknowledgement and the decision -- each carrying
// its message text, kept for ever. A season is a few hundred megabytes and
// nothing ever removed them (docs/PLATFORM_SCALE_REVIEW.md, A6).
//
// # Why only the delivered ones, and only after a year
//
// What is queued, failed or abandoned is work or a problem, and stays until
// somebody has dealt with it. What was delivered is a record of what a farmer
// was told, and the question "what was I told" belongs to the season it was
// told in: thirteen months outlives any appeal against that season's claims.
// The claim itself, its decision and the audit trail are untouched.
func (q *Queue) SweepSent(ctx context.Context, keep time.Duration) (int64, error) {
	if keep <= 0 {
		keep = 400 * 24 * time.Hour
	}
	var total int64
	for {
		tag, err := q.pool.Exec(ctx, `
			DELETE FROM notifications
			 WHERE ctid IN (
			     SELECT ctid FROM notifications
			      WHERE state = 'sent' AND sent_at < now() - $1::interval
			      LIMIT 5000)`, keep.String())
		if err != nil {
			return total, fmt.Errorf("sweeping delivered notifications: %w", err)
		}
		total += tag.RowsAffected()
		if tag.RowsAffected() < 5000 {
			return total, nil
		}
		if err := ctx.Err(); err != nil {
			return total, err
		}
	}
}

// Run drains the queue until the context is cancelled.
//
// # Why a loop rather than sending at enqueue time
//
// The outbox only pays for itself if something separate delivers it.
// Enqueueing inside the caller's transaction is half the pattern; this is the
// other half, and without it the table fills and nothing leaves.
//
// # Why it is quiet with no provider
//
// It returns immediately rather than spinning. A deployment with no gateway
// is the normal state today, and a worker logging "no provider" every
// interval would train everyone to ignore the log that will eventually
// carry a real failure.
func (q *Queue) Run(ctx context.Context, every time.Duration) {
	if q.sender == nil {
		q.log.Info("notification dispatcher idle: no provider is configured")
		return
	}
	if every <= 0 {
		every = 30 * time.Second
	}

	t := time.NewTicker(every)
	defer t.Stop()

	q.log.Info("notification dispatcher started", "interval", every,
		"provider", q.sender.Name())

	for {
		select {
		case <-ctx.Done():
			q.log.Info("notification dispatcher stopped")
			return
		case <-t.C:
			// Drained, not sampled. A tick that sends its batch and waits for
			// the next tick delivers a batch per interval however much is
			// waiting -- so the backlog after a district-wide event would
			// leave later claims acknowledged hours after they were filed.
			// Bounded by rounds so a permanently full queue cannot starve the
			// shutdown path.
			for round := 0; round < 100; round++ {
				sent, err := q.Dispatch(ctx, dispatchBatch)
				if errors.Is(err, ErrNoSender) {
					return
				}
				if err != nil {
					// Logged and retried on the next tick. A dispatch that
					// fails wholesale is usually the database or the gateway,
					// and both recover on their own more often than not.
					q.log.Error("dispatching notifications", "error", err)
					break
				}
				if sent > 0 {
					q.log.Info("notifications sent", "count", sent)
				}
				if sent < dispatchBatch || ctx.Err() != nil {
					break
				}
			}
		}
	}
}
