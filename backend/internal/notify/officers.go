package notify

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/agribid/esurvey-platform/backend/internal/platform/db"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// The console's own bell.
//
// # Why this is not the queue above it
//
// The rest of this package tells a *farmer*, by SMS, through a gateway that
// may not be connected. This tells an *officer*, inside the console they are
// already signed in to. Different recipient, different channel, different
// failure mode: an SMS that cannot be sent is a person not told, whereas a
// notification is read from the database on the next page load, so there is
// nothing to queue and nothing to fail.
//
// They share a package because they answer one question -- who has this
// platform failed to tell something -- and splitting them would let the two
// answers drift.
//
// # Why rows per officer rather than a view over the work
//
// "Reports waiting at your rung" is already a query; the bell could have been
// that query with a count. It is not, because *unread* is per person and has
// to survive the thing moving on. An officer who saw a report arrive, went to
// lunch, and came back after a colleague adjudicated it should not have the
// notification vanish -- they should see that it arrived and that it is dealt
// with. A view cannot say that; a row that was written once and read once can.

// Notification kinds. Stored, so they outlive any constant here.
const (
	KindReportAwaiting  = "report.awaiting_you"
	KindReportReturned  = "report.returned"
	KindReportDecided   = "report.decided"
	KindEventOpened     = "event.opened"
	KindEventPaused     = "event.paused"
	KindEventClosing    = "event.closing"
	KindGrievanceFiled  = "grievance.filed"
	KindAccountInvited  = "account.invited"
	KindAccountDisabled = "account.disabled"
)

// Notification is one thing an officer has not yet seen.
type Notification struct {
	ID     string     `json:"id"`
	Kind   string     `json:"kind"`
	Title  string     `json:"title"`
	Body   string     `json:"body,omitempty"`
	Link   string     `json:"link,omitempty"`
	Read   bool       `json:"read"`
	At     time.Time  `json:"created_at"`
	ReadAt *time.Time `json:"read_at,omitempty"`

	ResourceType string `json:"resource_type,omitempty"`
	ResourceID   string `json:"resource_id,omitempty"`
}

// New is one notification to record.
type New struct {
	UserID string
	Kind   string
	Title  string
	Body   string
	Link   string

	ResourceType string
	ResourceID   string
}

// Officers records and reads officer notifications.
type Officers struct{ pool *db.Pool }

func NewOfficers(pool *db.Pool) *Officers { return &Officers{pool: pool} }

// Record writes one notification, ignoring a repeat of the same unread fact.
//
// The ON CONFLICT is the whole reliability story. Anything that notices work
// has arrived may run more than once -- a retry, a second API instance, a
// poller that re-reads the same page -- and without it an officer returning
// from lunch finds the same report announced eleven times and stops reading
// the bell, which is the only failure mode that actually matters here.
//
// Takes a Querier so the notification commits with the thing that caused it.
func (o *Officers) Record(ctx context.Context, q db.Querier, n New) error {
	if n.UserID == "" || n.Kind == "" || n.Title == "" {
		return errors.New("notify: a notification needs a recipient, a kind and a title")
	}
	var resourceID any
	if n.ResourceID != "" {
		resourceID = n.ResourceID
	}
	_, err := q.Exec(ctx, `
		INSERT INTO officer_notifications
		       (user_id, kind, title, body, link, resource_type, resource_id)
		VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), NULLIF($6, ''), $7)
		ON CONFLICT (user_id, kind, resource_type, resource_id)
		    WHERE resource_id IS NOT NULL AND read_at IS NULL
		    DO NOTHING`,
		n.UserID, n.Kind, n.Title, n.Body, n.Link, n.ResourceType, resourceID)
	if err != nil {
		return fmt.Errorf("recording a notification: %w", err)
	}
	return nil
}

// RecordMany writes one notification per recipient.
func (o *Officers) RecordMany(ctx context.Context, q db.Querier, userIDs []string, n New) error {
	for _, id := range userIDs {
		n.UserID = id
		if err := o.Record(ctx, q, n); err != nil {
			return err
		}
	}
	return nil
}

// Recipients lists the officers who should hear about something happening in
// a jurisdiction, filtered to those whose role carries a permission.
//
// Resolved from live postings at the moment of the event rather than from a
// subscription list, because a posting is the subscription: an officer
// transferred out yesterday should not be told about today's report, and one
// posted in this morning should.
func (o *Officers) Recipients(
	ctx context.Context, q db.Querier, jurisdictionID string, roles []rbac.Role,
) ([]string, error) {
	if jurisdictionID == "" || len(roles) == 0 {
		return nil, nil
	}
	names := make([]string, len(roles))
	for i, r := range roles {
		names[i] = string(r)
	}

	// The posting covers the jurisdiction when it *is* that node or sits
	// above it -- a Tehsildar hears about a report in any village of their
	// tehsil, and a Talathi hears only about their own.
	rows, err := q.Query(ctx, `
		SELECT DISTINCT ur.user_id
		  FROM user_roles ur
		  JOIN users u ON u.id = ur.user_id AND u.status = 'active'
		  JOIN jurisdictions target ON target.id = $1
		 WHERE ur.revoked_at IS NULL
		   AND ur.role::text = ANY($2::text[])
		   AND (ur.jurisdiction_id = target.id
		        OR ur.jurisdiction_id = ANY(target.ancestor_ids))`,
		jurisdictionID, names)
	if err != nil {
		return nil, fmt.Errorf("finding notification recipients: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ListFilter narrows a bell listing.
type ListFilter struct {
	UserID     string
	UnreadOnly bool
	Limit      int
}

// List returns an officer's notifications, newest first.
func (o *Officers) List(ctx context.Context, f ListFilter) ([]Notification, error) {
	if f.Limit <= 0 || f.Limit > 100 {
		// The bell is a panel, not a page. An officer who needs the whole
		// history of what arrived is looking for the queue itself, which has
		// its own page with filters and a pager.
		f.Limit = 30
	}
	rows, err := o.pool.Query(ctx, `
		SELECT id, kind, title, COALESCE(body, ''), COALESCE(link, ''),
		       COALESCE(resource_type, ''), COALESCE(resource_id::text, ''),
		       created_at, read_at
		  FROM officer_notifications
		 WHERE user_id = $1
		   AND ($2::boolean = false OR read_at IS NULL)
		 ORDER BY created_at DESC
		 LIMIT $3`, f.UserID, f.UnreadOnly, f.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Notification{}
	for rows.Next() {
		var n Notification
		if err := rows.Scan(&n.ID, &n.Kind, &n.Title, &n.Body, &n.Link,
			&n.ResourceType, &n.ResourceID, &n.At, &n.ReadAt); err != nil {
			return nil, err
		}
		n.Read = n.ReadAt != nil
		out = append(out, n)
	}
	return out, rows.Err()
}

// Unread counts what is waiting, for the badge on the bell.
func (o *Officers) Unread(ctx context.Context, userID string) (int, error) {
	var n int
	err := o.pool.QueryRow(ctx, `
		SELECT count(*) FROM officer_notifications
		 WHERE user_id = $1 AND read_at IS NULL`, userID).Scan(&n)
	return n, err
}

// MarkRead marks one notification read, for this officer only.
//
// The user_id in the WHERE clause is the authorization check: without it, an
// id from another officer's bell would be marked read by whoever guessed it.
func (o *Officers) MarkRead(ctx context.Context, userID, id string) error {
	tag, err := o.pool.Exec(ctx, `
		UPDATE officer_notifications SET read_at = now()
		 WHERE id = $1 AND user_id = $2 AND read_at IS NULL`, id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// Already read, or not theirs. Both are "nothing to do" and neither
		// is worth distinguishing to the caller -- the second one would
		// confirm that somebody else's notification exists.
		return nil
	}
	return nil
}

// MarkAllRead clears an officer's bell.
func (o *Officers) MarkAllRead(ctx context.Context, userID string) (int, error) {
	tag, err := o.pool.Exec(ctx, `
		UPDATE officer_notifications SET read_at = now()
		 WHERE user_id = $1 AND read_at IS NULL`, userID)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// ByID reads one of an officer's own notifications.
func (o *Officers) ByID(ctx context.Context, userID, id string) (*Notification, error) {
	var n Notification
	err := o.pool.QueryRow(ctx, `
		SELECT id, kind, title, COALESCE(body, ''), COALESCE(link, ''),
		       COALESCE(resource_type, ''), COALESCE(resource_id::text, ''),
		       created_at, read_at
		  FROM officer_notifications
		 WHERE id = $1 AND user_id = $2`, id, userID).
		Scan(&n.ID, &n.Kind, &n.Title, &n.Body, &n.Link,
			&n.ResourceType, &n.ResourceID, &n.At, &n.ReadAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	n.Read = n.ReadAt != nil
	return &n, nil
}

// ErrNotFound is a notification that is not this officer's, or not there.
var ErrNotFound = errors.New("no such notification")

// RecipientsUnder lists officers posted at or beneath any of the given nodes.
//
// The mirror of Recipients, and needed because the two questions this package
// answers point in opposite directions down the tree.
//
// A report arrives at a rung and the officers to tell are the ones posted
// *over* its village -- a Tehsildar hears about a report in any village of
// their tehsil. An event is declared over a taluka and the officers to tell
// are the ones posted *under* it: the Talathis and Krushi Sahayaks who will do
// the surveying, who are three levels below the area named in the
// notification.
//
// Using Recipients for the second case would have told the Collector who
// declared the event and nobody who has to act on it.
func (o *Officers) RecipientsUnder(
	ctx context.Context, q db.Querier, ancestorIDs []string, roles []rbac.Role,
) ([]string, error) {
	if len(ancestorIDs) == 0 || len(roles) == 0 {
		return nil, nil
	}
	names := make([]string, len(roles))
	for i, r := range roles {
		names[i] = string(r)
	}

	rows, err := q.Query(ctx, `
		SELECT DISTINCT ur.user_id
		  FROM user_roles ur
		  JOIN users u ON u.id = ur.user_id AND u.status = 'active'
		  JOIN jurisdictions j ON j.id = ur.jurisdiction_id
		 WHERE ur.revoked_at IS NULL
		   AND ur.role::text = ANY($2::text[])
		   AND (j.id = ANY($1::uuid[]) OR j.ancestor_ids && $1::uuid[])`,
		ancestorIDs, names)
	if err != nil {
		return nil, fmt.Errorf("finding notification recipients: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// FieldRoles are the officers who do the surveying: the village pair and the
// Circle Officer above them. Who an event being opened, paused or about to
// close is actually news to.
var FieldRoles = []rbac.Role{
	rbac.RoleTalathi, rbac.RoleKrushiSahayak, rbac.RoleMandalOfficer,
}

// KindReportOverdue is a report that has sat at a rung past its allowance.
//
// Its own kind rather than a second "awaiting you": an officer scanning the
// bell needs to tell "this arrived" from "this has been here too long", and
// the second is the one that should change what they do this morning.
const KindReportOverdue = "report.overdue"
