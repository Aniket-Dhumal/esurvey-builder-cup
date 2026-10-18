package report

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/agribid/esurvey-platform/backend/internal/notify"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// Telling the officers whose queue just changed.
//
// # Why this is not the outbox above it
//
// internal/notify's queue tells a *farmer*, by SMS, and may fail because a
// gateway is not connected. This tells an *officer*, in the console they are
// already signed in to, and cannot fail for any reason worth losing a report
// over.
//
// Both are written inside the caller's transaction, and for the same reason:
// the intent to tell somebody has to commit with the thing that caused it. A
// report that moved to the tehsil with no notification queued -- because the
// process died between the two writes -- is a report sitting in a queue nobody
// was told about, which is exactly the failure an approval ladder cannot
// afford.
//
// # Why a role, and not a person
//
// A report arrives at a *rung*, not at an officer. Whoever holds the tehsil
// this morning is who should be told, and if that officer is transferred at
// noon the notification should already have gone to the person who was there.
// Resolving recipients from live postings at the moment of the move is what
// makes that true, and it is why there is no subscription table here.

// rungRoles is who sits at each rung of the ladder.
//
// The village pair is two roles because the joint panchnama is signed by the
// revenue side and the agriculture side together -- a report returned to the
// pair has to reach both, not whichever of them the code happened to name.
var rungRoles = map[Level][]rbac.Role{
	LevelVillagePair: {rbac.RoleTalathi, rbac.RoleKrushiSahayak},
	LevelCircle:      {rbac.RoleMandalOfficer},
	LevelTehsil:      {rbac.RoleTehsildar},
	LevelDistrict:    {rbac.RoleDistrictMagistrate},
}

var rungName = map[Level]string{
	LevelVillagePair: "the village pair",
	LevelCircle:      "the circle",
	LevelTehsil:      "the tehsil",
	LevelDistrict:    "the district",
}

// WithOfficerNotices attaches the console's bell.
//
// Optional, like the SMS outbox: a deployment without it simply records
// nothing, which is the state this platform was in before the bell existed.
func (s *Store) WithOfficerNotices(o *notify.Officers) *Store {
	s.officers = o
	return s
}

// tellRung records a notification for every officer holding a rung over the
// report's village.
//
// Never fails the caller. A report that moved correctly and could not be
// announced is still a report that moved; losing the move to save the
// announcement would be the wrong way round -- the same rule the farmer outbox
// follows. The error is returned for the caller to log, not to abort on.
func (s *Store) tellRung(
	ctx context.Context, tx pgx.Tx, reportID string, rung Level, kind, title, body string,
) error {
	if s.officers == nil {
		return nil
	}
	roles, ok := rungRoles[rung]
	if !ok {
		return nil
	}

	// The report's village, as a node in the tree. Reports carry the LGD code
	// rather than a foreign key -- they record the place as it was named when
	// filed -- so the join is on the code.
	var villageID, reference string
	err := tx.QueryRow(ctx, `
		SELECT v.id, r.reference
		  FROM crop_loss_reports r
		  JOIN jurisdictions v
		    ON v.code = r.village_lgd_code AND v.level = 'village'
		 WHERE r.id = $1`, reportID).Scan(&villageID, &reference)
	if err != nil {
		return fmt.Errorf("resolving the report's village: %w", err)
	}

	to, err := s.officers.Recipients(ctx, tx, villageID, roles)
	if err != nil {
		return err
	}
	if len(to) == 0 {
		// A rung with nobody posted to it. Worth saying out loud rather than
		// passing over: a report has arrived somewhere no officer can act on
		// it, and it will sit there until somebody is posted.
		return fmt.Errorf("no active officer holds %s over village %s",
			rungName[rung], villageID)
	}

	return s.officers.RecordMany(ctx, tx, to, notify.New{
		Kind:  kind,
		Title: fmt.Sprintf(title, reference),
		Body:  body,
		// Straight to the queue it landed in, not to the report on its own:
		// the officer's next action is to work the queue, and a notification
		// that lands them somewhere with no next step is a notification that
		// wasted their time.
		Link:         linkFor(rung),
		ResourceType: "crop_loss_report",
		ResourceID:   reportID,
	})
}

func linkFor(rung Level) string {
	if rung == LevelVillagePair {
		return "/worklist"
	}
	return "/approvals"
}

// arrived announces a report reaching a new rung.
func (s *Store) arrived(ctx context.Context, tx pgx.Tx, reportID string, rung Level) error {
	return s.tellRung(ctx, tx, reportID, rung,
		notify.KindReportAwaiting,
		"%s is waiting for your approval",
		"It has been approved at the level below and is now at "+rungName[rung]+".")
}

// returned announces a report sent back down for correction.
//
// Deliberately a different kind from an arrival, and not because the wording
// differs: a returned report is one an officer has already looked at and been
// told to look at again, and an officer scanning their bell needs to tell the
// two apart at a glance.
func (s *Store) returned(ctx context.Context, tx pgx.Tx, reportID string, to Level, reason string) error {
	body := "It was sent back for correction."
	if reason != "" {
		body = "Sent back: " + reason
	}
	return s.tellRung(ctx, tx, reportID, to,
		notify.KindReportReturned,
		"%s was returned to you",
		body)
}

// noticeFailed records a notification that could not be written.
//
// Not returned to the caller, and not silent either. The decision itself is
// sound and must commit; what is lost is that somebody was told about it, and
// that is worth a line in the log rather than a rolled-back approval.
//
// A store with no logger says nothing, which is the same contract the farmer
// outbox has.
func (s *Store) noticeFailed(what, reportID string, err error) {
	if s.log == nil {
		return
	}
	s.log.Warn(what, "report_id", reportID, "error", err)
}

// --- grievances ------------------------------------------------------------

// grievanceDeciders are the officers who can answer an appeal.
//
// Not the village pair, and not the Circle Officer. Their verification is
// usually the thing being appealed, and an appeal answered by the person
// appealed against is not an appeal -- the same reasoning that keeps
// grievance:decide off those roles in the permission matrix.
var grievanceDeciders = []rbac.Role{
	rbac.RoleTehsildar, rbac.RoleDistrictMagistrate,
}

// grievanceFiled tells the officers who can answer it.
//
// The farmer is already told, by SMS, on the queue above. This is the other
// half: an appeal nobody has been told about is an appeal that sits until its
// deadline passes, which is precisely the failure the appeal exists to end.
//
// Best-effort, and outside the insert's own statement: the grievance is
// recorded whether or not anyone could be notified, and the alternative --
// refusing the appeal because the bell could not be written -- would be the
// worst possible trade for the farmer making it.
func (s *Store) grievanceFiled(
	ctx context.Context, villageCode, reference, ground, grievanceID string,
) error {
	if s.officers == nil {
		return nil
	}

	var villageID string
	err := s.pool.QueryRow(ctx,
		`SELECT id FROM jurisdictions WHERE code = $1 AND level = 'village'`,
		villageCode).Scan(&villageID)
	if err != nil {
		return fmt.Errorf("resolving the grievance's village: %w", err)
	}

	to, err := s.officers.Recipients(ctx, s.pool, villageID, grievanceDeciders)
	if err != nil {
		return err
	}
	if len(to) == 0 {
		return fmt.Errorf("no active officer can decide grievances over village %s",
			villageCode)
	}

	return s.officers.RecordMany(ctx, s.pool, to, notify.New{
		Kind:         notify.KindGrievanceFiled,
		Title:        fmt.Sprintf("%s: a farmer has appealed", reference),
		Body:         "Ground: " + ground + ". It has a deadline; an unanswered appeal is worse for a farmer than a rejected one.",
		Link:         "/grievances",
		ResourceType: "grievance",
		ResourceID:   grievanceID,
	})
}
