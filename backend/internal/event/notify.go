package event

import (
	"context"
	"fmt"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/notify"
	"github.com/agribid/esurvey-platform/backend/internal/platform/db"
)

// Telling the officers who have to act on an event.
//
// # Who is told, and why it is not who declared it
//
// An event is declared by a Collector over a taluka. The people whose work it
// changes are three levels below: the Talathis and Krushi Sahayaks who will
// survey, and the Circle Officers above them. So recipients are resolved
// *downward* from the areas the event names -- see RecipientsUnder -- rather
// than upward the way a report's arrival at a rung is.
//
// # Why opening and pausing both notify, and closing does not
//
// Opening and pausing change what an officer may do this morning: one starts
// collection, the other stops it, and a Talathi who drives out to a village
// and cannot file has been failed by the platform. Closing is the deadline
// arriving on the date everyone was already told about, so it is announced
// *before* it happens -- see SweepClosing -- and not at the moment it does.

// WithOfficerNotices attaches the console's bell.
//
// Optional. A deployment without it records nothing, which is the state this
// platform was in before the bell existed.
func (s *Store) WithOfficerNotices(o *notify.Officers) *Store {
	s.officers = o
	return s
}

// areaIDs are the jurisdictions an event covers.
func (s *Store) areaIDs(ctx context.Context, q db.Querier, eventID string) ([]string, error) {
	rows, err := q.Query(ctx,
		`SELECT jurisdiction_id::text FROM survey_event_areas WHERE event_id = $1`,
		eventID)
	if err != nil {
		return nil, err
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

// tellField records a notification for the officers who work an event's area.
//
// Best-effort by design. An event that opened correctly and could not be
// announced is still an open event; refusing the transition to save the
// notification would be the wrong way round.
func (s *Store) tellField(ctx context.Context, e *Event, kind, title, body, link string) error {
	if s.officers == nil {
		return nil
	}
	areas, err := s.areaIDs(ctx, s.pool, e.ID)
	if err != nil {
		return fmt.Errorf("reading the event's areas: %w", err)
	}
	if len(areas) == 0 {
		return nil
	}

	to, err := s.officers.RecipientsUnder(ctx, s.pool, areas, notify.FieldRoles)
	if err != nil {
		return err
	}
	if len(to) == 0 {
		// Worth saying out loud. An event has opened over ground with no
		// surveying officer posted to it, and no farmer in those villages
		// will be visited until somebody is.
		return fmt.Errorf(
			"event %s covers areas with no active surveying officer posted",
			e.Reference)
	}

	return s.officers.RecordMany(ctx, s.pool, to, notify.New{
		Kind:         kind,
		Title:        title,
		Body:         body,
		Link:         link,
		ResourceType: "survey_event",
		ResourceID:   e.ID,
	})
}

// announce reports a state change to the officers in the field.
func (s *Store) announce(ctx context.Context, e *Event, to State) error {
	switch to {
	case StateOpen:
		return s.tellField(ctx, e,
			notify.KindEventOpened,
			fmt.Sprintf("Surveying is open for %s", e.Reference),
			fmt.Sprintf("%s. Filing closes %s.",
				e.Title, e.SurveyClosesAt.Format("2 January 2006")),
			"/events")
	case StatePaused:
		return s.tellField(ctx, e,
			notify.KindEventPaused,
			fmt.Sprintf("Filing is paused for %s", e.Reference),
			"The district has suspended new reports. The deadline has not "+
				"changed, and reports already filed are unaffected.",
			"/events")
	}
	return nil
}

// SweepClosing warns the field that a filing window is about to end.
//
// # Why this is a sweep and not an event
//
// Nothing happens when a deadline approaches. There is no state change to
// hang a notification on -- the event simply gets closer to a date that was
// fixed when it was declared -- so somebody has to look.
//
// # Why re-running it does not spam
//
// The dedupe index on officer_notifications covers (user, kind, resource)
// while unread, so a sweep that runs every hour records the first warning and
// nothing after it. An officer who reads and dismisses the warning can be
// warned again, which is the behaviour worth having: they have seen it, and
// the deadline is now nearer.
func (s *Store) SweepClosing(ctx context.Context, within time.Duration) (int, error) {
	if s.officers == nil {
		return 0, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, reference, title, survey_closes_at
		  FROM survey_events
		 WHERE state IN ('open', 'paused')
		   AND survey_closes_at > now()
		   AND survey_closes_at <= now() + $1::interval
		 ORDER BY survey_closes_at`, within.String())
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	type closing struct {
		id, ref, title string
		at             time.Time
	}
	var due []closing
	for rows.Next() {
		var c closing
		if err := rows.Scan(&c.id, &c.ref, &c.title, &c.at); err != nil {
			return 0, err
		}
		due = append(due, c)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	n := 0
	// "Tomorrow" is a calendar fact, not a 24-hour one. Measured in elapsed
	// hours, a deadline 47 hours away truncates to one day and gets announced
	// as tomorrow when it is the day after -- and an officer who reorganises
	// their morning around that has been misled by an integer division.
	//
	// Compared as dates, in the local zone the officer's date is written in.
	today := time.Now().Truncate(24 * time.Hour)
	for _, c := range due {
		days := int(c.at.Local().Truncate(24*time.Hour).Sub(today).Hours() / 24)
		when := fmt.Sprintf("in %d days", days)
		switch {
		case days <= 0:
			when = "today"
		case days == 1:
			when = "tomorrow"
		}
		e := &Event{ID: c.id, Reference: c.ref, Title: c.title, SurveyClosesAt: c.at}
		if err := s.tellField(ctx, e,
			notify.KindEventClosing,
			fmt.Sprintf("Filing for %s closes %s", c.ref, when),
			fmt.Sprintf("%s. Reports not filed by %s cannot be filed against "+
				"this event.", c.title, c.at.Format("2 January 2006")),
			"/events"); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// SweepExpired closes events whose filing deadline has passed.
//
// # Why this is not left to an officer
//
// It was. An event stayed `open` until somebody closed it by hand, which
// meant the deadline printed on the farmer's copy and shown in the console
// was advisory: reports could be filed weeks past it, and were.
//
// report.Create now refuses a late filing on its own, so this is not what
// stops one. What it does is make the record say what happened -- an event
// past its deadline reads as `closed` rather than as open-and-unattended --
// and it frees the district from having to remember on the day.
//
// # Why closed_by is left null
//
// Because nobody closed it. The column is nullable exactly so that a close
// with no officer behind it is expressible rather than attributed to whoever
// happened to run the sweep, and the audit line says the deadline did it.
//
// A district that wants a claim in after this reopens the event, which is a
// decision with a name on it.
func (s *Store) SweepExpired(ctx context.Context) (int, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE survey_events
		   SET state = 'closed', closed_at = now()
		 WHERE state IN ('open', 'paused')
		   AND survey_closes_at <= now()
		RETURNING id, reference, title`)
	if err != nil {
		return 0, fmt.Errorf("closing expired events: %w", err)
	}
	defer rows.Close()

	type closed struct{ id, ref, title string }
	var done []closed
	for rows.Next() {
		var c closed
		if err := rows.Scan(&c.id, &c.ref, &c.title); err != nil {
			return 0, err
		}
		done = append(done, c)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	for _, c := range done {
		if s.log != nil {
			s.log.Info("filing closed by its deadline",
				"event", c.ref, "title", c.title)
		}
		// The field is told, because a Talathi holding an unfiled report
		// needs to know the window has gone rather than discovering it at a
		// counter with a farmer in front of them.
		e := &Event{ID: c.id, Reference: c.ref, Title: c.title}
		if err := s.tellField(ctx, e,
			notify.KindEventClosing,
			fmt.Sprintf("Filing for %s has closed", c.ref),
			c.title+". No further reports can be filed against this event. "+
				"The district can reopen it if a claim still has to be accepted.",
			"/events"); err != nil && s.log != nil {
			s.log.Warn("announcing a closed event", "event", c.ref, "error", err)
		}
	}
	return len(done), nil
}
