package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/agribid/esurvey-platform/backend/internal/agristack"
	"github.com/agribid/esurvey-platform/backend/internal/audit"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// Scope checks answer the "where" half of an authorization decision.
//
// The "what" half is settled at the route by require(), which is cheap and
// needs nothing but the token. This half needs the resource, so it lives at
// the point the resource is loaded -- and it is written once, here, because a
// containment check copied into each handler is a containment check that will
// eventually differ in one of them.

// errScope is returned when a resource lies outside the caller's reach. It is
// deliberately vague to the caller; the audit entry carries the detail.
var errScope = errors.New("out of scope")

// assertInScope checks that a village lies inside the principal's postings.
func (s *Server) assertInScope(
	r *http.Request, p *rbac.Principal, villageCode, resourceType, resourceID string,
) error {
	if p == nil {
		return rbac.ErrUnauthenticated
	}

	// A farmer is not posted anywhere. Their reach is their own records, and
	// the ownership check that establishes it is the caller's job -- there is
	// nothing sensible for a jurisdiction test to say here.
	if p.IsFarmer() {
		return nil
	}

	scope := p.Scope()
	if len(scope.JurisdictionIDs) == 0 {
		s.denyScope(r, p, villageCode, resourceType, resourceID, "no jurisdiction")
		return errScope
	}

	ok, err := s.Identity.VillageInScope(r.Context(), villageCode, scope.JurisdictionIDs)
	if err != nil {
		return fmt.Errorf("checking jurisdiction: %w", err)
	}
	if !ok {
		s.denyScope(r, p, villageCode, resourceType, resourceID, "outside posting")
		return errScope
	}
	return nil
}

// assertFarmerInScope checks a registry record.
//
// A farmer reads their own record and no one else's; an officer reads the
// records in the villages they are posted to.
func (s *Server) assertFarmerInScope(
	r *http.Request, p *rbac.Principal, rec *agristack.FarmerRecord,
) error {
	if p == nil {
		return rbac.ErrUnauthenticated
	}

	if p.IsFarmer() {
		if p.FarmerID != "" && p.FarmerID == rec.Farmer.FarmerID {
			return nil
		}
		s.denyScope(r, p, rec.Farmer.VillageLGDCode,
			"farmer", rec.Farmer.FarmerID, "not the account holder")
		return errScope
	}

	return s.assertInScope(r, p, rec.Farmer.VillageLGDCode,
		"farmer", rec.Farmer.FarmerID)
}

func (s *Server) denyScope(
	r *http.Request, p *rbac.Principal, villageCode, resourceType, resourceID, reason string,
) {
	s.Audit.Record(r.Context(), audit.Event{
		Action:  audit.ActionAuthzDenied,
		Outcome: audit.OutcomeDeny,
		ActorIP: clientIP(r, s.TrustedProxyHops),
		// Recorded against the resource's jurisdiction rather than the
		// actor's: "who reached into this village from outside" is the query
		// that matters afterwards.
		JurisdictionCode: villageCode,
		ResourceType:     resourceType,
		ResourceID:       resourceID,
		RequestID:        RequestID(r.Context()),
		Detail: map[string]any{
			"reason":              reason,
			"village_lgd_code":    villageCode,
			"actor_jurisdictions": p.JurisdictionCodes(),
		},
	}.FromContext(r.Context()))
}

func (s *Server) writeScopeError(w http.ResponseWriter, err error) {
	// A malformed id is the caller's mistake; see ids.go. Checked first so it
	// cannot fall through to the 500 below.
	if badRequestFromDB(w, err) {
		return
	}
	switch {
	case errors.Is(err, errScope):
		writeError(w, http.StatusForbidden, "OUT_OF_JURISDICTION",
			"this record is outside the area you are posted to")
	case errors.Is(err, rbac.ErrUnauthenticated):
		writeError(w, http.StatusUnauthorized, "AUTH_REQUIRED",
			"this endpoint requires an access token")
	default:
		s.Log.Error("scope check failed", "error", err)
		writeError(w, http.StatusInternalServerError, "SCOPE_CHECK_FAILED",
			"the request could not be completed")
	}
}
