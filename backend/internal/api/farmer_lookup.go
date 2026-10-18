package api

import (
	"context"
	"errors"
	"fmt"
	"github.com/agribid/esurvey-platform/backend/internal/agristack"
	"net/http"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/agribid/esurvey-platform/backend/internal/audit"
	"github.com/agribid/esurvey-platform/backend/internal/auth"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// Finding a farmer's own record when the registry's mobile number is not the
// one in their hand.
//
// # Why this route exists at all
//
// The strict rule -- you may read the record whose registered mobile sent you
// the code -- is the safest one and it locks out a large number of the people
// this app is for. AgriStack holds whatever number was given at enrolment,
// which is routinely a number since lost, a relative's, or a shopkeeper's. A
// farmer standing in a hailed field cannot re-enrol to claim for it.
//
// So a farmer may identify a record another way: by naming it and answering a
// question about it. It was decided on 2026-09-16 that the question is the
// date of birth the registry holds.
//
// # What that costs, stated plainly
//
// The Farmer ID is not a secret -- it is printed on documents and known
// around a village -- so the date of birth is carrying the whole weight. It
// is one low-entropy fact that a neighbour or a relative may simply know, and
// in this registry's own sample data it is 1 January more often than chance
// would have it, because that is what gets entered when nobody asks.
//
// This route is therefore built as though it will be guessed at:
//
//   - every failure is counted against the account, and a handful of them
//     stops it trying for a while (lookupFailureBudget);
//   - the counter fails CLOSED, unlike the general rate limiter, because a
//     cache outage that opens an unlimited guessing window is worse than a
//     farmer being told to try again in a minute;
//   - a wrong id, an unknown id and a wrong date all answer identically, so
//     nothing here can be used to discover which Farmer IDs exist or which
//     dates are close;
//   - both outcomes are audited, so a pattern of attempts is visible after
//     the fact even though no attempt is visible during it.
//
// # What comes back
//
// The projected record, and a farmer grant (see auth.IssueFarmerGrant) which
// the handset stores beside that farmer and presents when refreshing it. The
// link between an account and the farmers it acts for lives on the phone by
// design, so the grant is what carries it back here.

// lookupFailureBudget is how many wrong answers an account may give before it
// is made to wait. Five, against roughly fourteen thousand candidate dates:
// enough for a farmer who mistypes or misremembers the format, far too few to
// work through a year.
const lookupFailureBudget = 5

// lookupFailureWindow is how long the count and the lockout last.
const lookupFailureWindow = 15 * time.Minute

type registryLookupRequest struct {
	FarmerID string `json:"farmer_id"`
	// DOB as the farmer knows it. Accepted in the formats a person or a
	// registry actually writes; never logged, and never echoed back.
	DOB string `json:"dob"`
}

// myRegistryLookup identifies one farmer record from an id and a date.
func (s *Server) myRegistryLookup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := rbac.FromContext(ctx)

	if !p.IsFarmer() {
		writeError(w, http.StatusForbidden, "FARMER_ONLY",
			"this route serves a farmer their own record; officers use /api/v1/farmers")
		return
	}

	var req registryLookupRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}
	req.FarmerID = strings.TrimSpace(req.FarmerID)
	req.DOB = strings.TrimSpace(req.DOB)
	if req.FarmerID == "" || req.DOB == "" {
		writeError(w, http.StatusBadRequest, "MISSING_FIELD",
			"farmer_id and dob are required")
		return
	}

	// Checked before the registry is touched, so a locked-out account cannot
	// even learn how long a lookup takes.
	locked, err := s.lookupLocked(ctx, p.UserID)
	if err != nil {
		// Fails closed. See the note at the top of this file.
		s.Log.Warn("farmer lookup limiter unavailable", "error", err)
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusServiceUnavailable, "TRY_AGAIN",
			"This cannot be checked just now. Try again in a minute.")
		return
	}
	if locked {
		w.Header().Set("Retry-After", fmt.Sprint(int(lookupFailureWindow.Seconds())))
		writeError(w, http.StatusTooManyRequests, "TOO_MANY_ATTEMPTS",
			"Too many attempts. Try again in fifteen minutes.")
		return
	}

	rec, recErr := s.Registry.FarmerRecord(ctx, req.FarmerID)

	// The registry not answering is not a wrong guess. Counting it would lock
	// out every farmer who tried during an outage.
	if errors.Is(recErr, agristack.ErrUnavailable) {
		writeRegistryUnavailable(w)
		return
	}

	// One answer for "no such farmer" and "wrong date". Anything else turns
	// this into an oracle for which Farmer IDs exist.
	if recErr != nil || !sameDOB(rec.Farmer.DOB, req.DOB) {
		if err := s.countLookupFailure(ctx, p.UserID); err != nil {
			s.Log.Warn("could not count a farmer lookup failure", "error", err)
		}
		s.Audit.Record(ctx, audit.Event{
			Action: audit.ActionFarmerRead, Outcome: audit.OutcomeDeny,
			ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
			ResourceType: "farmer", ResourceID: req.FarmerID,
		}.FromContext(ctx))

		writeError(w, http.StatusNotFound, "FARMER_NOT_FOUND",
			"No record matches that Farmer ID and date of birth.")
		return
	}

	grant, expires, err := s.Auth.IssueFarmerGrant(p.UserID, rec.Farmer.FarmerID, auth.GrantTTL)
	if err != nil {
		s.Log.Error("could not issue a farmer grant", "error", err)
		writeError(w, http.StatusInternalServerError, "GRANT_FAILED",
			"could not confirm this farmer just now")
		return
	}

	s.Audit.Record(ctx, audit.Event{
		Action: audit.ActionFarmerRead, Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
		ResourceType: "farmer", ResourceID: rec.Farmer.FarmerID,
		JurisdictionCode: rec.Farmer.VillageLGDCode,
	}.FromContext(ctx))

	out := projectForFarmer(rec)
	out.FarmerGrant = grant
	out.GrantExpiresAt = expires
	writeJSON(w, http.StatusOK, out)
}

// sameDOB compares a date the registry holds with one a person typed.
//
// Both sides are normalised to a day rather than string-matched: the registry
// writes 01-01-1973, a handset date picker produces 1973-01-01, and a farmer
// reading off a document may type either with slashes. A date that cannot be
// read at all is not equal to anything, which is the safe direction.
func sameDOB(registry, given string) bool {
	a, aok := parseDay(registry)
	b, bok := parseDay(given)
	return aok && bok && a == b
}

// parseDay reads the formats this system actually meets. Deliberately not
// time.Parse with a single layout: the registry's own format and a phone's
// are different, and neither is negotiable from here.
func parseDay(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	s = strings.ReplaceAll(s, "/", "-")
	for _, layout := range []string{"02-01-2006", "2006-01-02", "2-1-2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format("2006-01-02"), true
		}
	}
	return "", false
}

// lookupLocked reports whether this account has spent its attempts.
func (s *Server) lookupLocked(ctx context.Context, userID string) (bool, error) {
	if s.Redis == nil {
		// No counter means no lockout, and no lockout means unlimited
		// guessing. A deployment without Redis does not get this route.
		return false, errors.New("no attempt counter configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	n, err := s.Redis.Get(ctx, lookupFailureKey(userID)).Int()
	if err != nil && err != redis.Nil {
		return false, err
	}
	return n >= lookupFailureBudget, nil
}

// countLookupFailure records one wrong answer.
//
// The window is set only when the key is new, so an account cannot hold its
// own lockout open or, more to the point, reset it by continuing to guess.
func (s *Server) countLookupFailure(ctx context.Context, userID string) error {
	if s.Redis == nil {
		return errors.New("no attempt counter configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	pipe := s.Redis.TxPipeline()
	pipe.Incr(ctx, lookupFailureKey(userID))
	pipe.ExpireNX(ctx, lookupFailureKey(userID), lookupFailureWindow)
	_, err := pipe.Exec(ctx)
	if err != nil && err != redis.Nil {
		return err
	}
	return nil
}

func lookupFailureKey(userID string) string { return "fg:fail:" + userID }

// errNotMyFarmer is the refusal when an account names a farmer it cannot act
// for. Deliberately not "not found": the caller supplied the id themselves
// and learns nothing from being told they may not use it.
var errNotMyFarmer = errors.New(
	"this account has not confirmed that farmer; add them first")

// actingFarmer says which registry identity a farmer account is acting for.
//
// # Why this is not simply the account's own id
//
// It was, and it is why one mobile number could hold exactly one farmer. A
// household shares a handset, and a farmer whose AgriStack number is long
// gone borrows one; both were shut out. An account may now act for any farmer
// it has answered for, and the grant it holds is what says which.
//
// # Why the body's id is refused rather than ignored
//
// The filing path used to overwrite whatever farmer_id arrived with the
// account's own -- safe when an account meant one farmer, and quietly wrong
// now: a claim meant for the second farmer in a household would be filed
// against the first, with the evidence attached to the wrong person's land.
// Silence is the worse failure here, so a farmer this session cannot act for
// is an error rather than a substitution.
func (s *Server) actingFarmer(r *http.Request, p *rbac.Principal, wanted string) (string, error) {
	if p == nil {
		return "", rbac.ErrUnauthenticated
	}
	if !p.IsFarmer() {
		return wanted, nil
	}
	grant := r.Header.Get("X-Farmer-Grant")

	// A grant names exactly one farmer, so presenting one is itself saying
	// which farmer this request is about. Without this every call site would
	// have to repeat the id in a query parameter beside the header, and the
	// one that forgot would silently act as the account's own farmer instead
	// -- resolving somebody else's claim to "not found" after the field had
	// been walked.
	if wanted == "" && grant != "" {
		if id, err := s.Auth.FarmerGranted(grant, p.UserID); err == nil && id != "" {
			return id, nil
		}
	}

	if wanted == "" || wanted == p.FarmerID {
		return p.FarmerID, nil
	}

	if grant == "" {
		return "", errNotMyFarmer
	}
	// The account binding is checked inside FarmerGranted: a grant lifted
	// from another handset names its own account and fails here.
	id, err := s.Auth.FarmerGranted(grant, p.UserID)
	if err != nil || id != wanted {
		return "", errNotMyFarmer
	}
	return wanted, nil
}

// actingScope is the caller's scope, narrowed to the farmer they are acting
// for.
//
// # Why this is not just p.Scope()
//
// A farmer's scope is one registry identity: the account's own. One mobile
// number may now act for several farmers, so every route that resolves a
// record through the raw scope reaches only the first of them -- and the
// symptom is not a refusal a farmer can understand. They file a claim for the
// second person in the household successfully, walk the field, and then every
// photograph is answered "no such report", because the claim they are
// uploading against is not one their scope can see.
//
// So the scope follows the grant, exactly as filing and listing do. An officer
// is unaffected: their reach is jurisdictions, not a farmer id.
//
// Errors are the caller's to answer, and mean the session named a farmer it
// may not act for.
func (s *Server) actingScope(r *http.Request, p *rbac.Principal) (rbac.Scope, error) {
	scope := p.Scope()
	if !p.IsFarmer() {
		return scope, nil
	}
	acting, err := s.actingFarmer(r, p, r.URL.Query().Get("farmer_id"))
	if err != nil {
		return scope, err
	}
	scope.FarmerID = acting
	return scope, nil
}
