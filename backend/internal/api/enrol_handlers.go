package api

import (
	"errors"
	"net/http"

	"github.com/agribid/esurvey-platform/backend/internal/audit"
	"github.com/agribid/esurvey-platform/backend/internal/auth"
	"github.com/agribid/esurvey-platform/backend/internal/identity"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// enrolFarmer creates a farmer's account from their registry record.
//
// Everything about the account comes from the registry: the name, the handset,
// the Aadhaar hash. Nothing is typed by the officer, because an account whose
// mobile number was typed at a counter is an account that can be pointed at
// somebody else's phone.
//
// The account has no password. A farmer signs in with a one-time code to the
// number the registry already holds, which is the only credential they have
// that this system can check.
func (s *Server) enrolFarmer(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := rbac.FromContext(ctx)
	farmerID := r.PathValue("farmerID")

	rec, err := s.Registry.FarmerRecord(ctx, farmerID)
	if err != nil {
		s.writeFarmerRecordError(w, err)
		return
	}

	// The officer must be posted where the farmer's land is.
	if err := s.assertFarmerInScope(r, p, rec); err != nil {
		s.writeScopeError(w, err)
		return
	}

	mobile, err := auth.NormalizeMobile(rec.Farmer.MobileNo)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "NO_USABLE_MOBILE",
			"the registry holds no usable mobile number for this farmer, so no "+
				"one-time code could reach them")
		return
	}

	// Already enrolled is not an error worth making an officer resolve.
	if existing, err := s.Identity.ByFarmerID(ctx, farmerID); err == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"user":         viewUser(existing),
			"already":      true,
			"sign_in_hint": "the farmer signs in with a one-time code to " + maskTail(mobile),
		})
		return
	}

	// The handset may already have signed in for itself, before anybody
	// enrolled this farmer. That account is this farmer's -- same number, same
	// person -- so it is named rather than duplicated, which the unique
	// handset column would refuse anyway.
	if held, err := s.Identity.ByMobile(ctx, mobile); err == nil {
		named, err := s.Identity.NameFarmer(ctx, held.ID, identity.NewFarmer{
			FullName:          rec.Farmer.FarmerName,
			MobileE164:        mobile,
			AgriStackFarmerID: farmerID,
			AadhaarHash:       rec.Farmer.AadhaarHash,
			AadhaarLast4:      rec.Farmer.Aadhaar,
		})
		if errors.Is(err, identity.ErrHandsetTaken) {
			writeError(w, http.StatusConflict, "HANDSET_TAKEN",
				"that handset is already signed in as a different farmer; the "+
					"registry's number for this farmer has to be corrected first")
			return
		}
		if err != nil {
			s.writeIdentityError(w, err)
			return
		}
		_ = s.Audit.RecordSync(ctx, audit.Event{
			Action: audit.ActionFarmerEnrolled, Outcome: audit.OutcomeAllow,
			ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
			ResourceType: "user", ResourceID: named.ID,
			JurisdictionCode: rec.Farmer.VillageLGDCode,
			Detail: map[string]any{
				"farmer_id": farmerID,
				"mobile":    maskTail(mobile),
				"how":       "named a handset that had already signed in",
			},
		}.FromContext(ctx))
		writeJSON(w, http.StatusOK, map[string]any{
			"user":         viewUser(named),
			"already":      true,
			"sign_in_hint": "the farmer signs in with a one-time code to " + maskTail(mobile),
		})
		return
	}

	user, err := s.Identity.CreateFarmer(ctx, identity.NewFarmer{
		FullName:          rec.Farmer.FarmerName,
		MobileE164:        mobile,
		AgriStackFarmerID: farmerID,
		AadhaarHash:       rec.Farmer.AadhaarHash,
		AadhaarLast4:      rec.Farmer.Aadhaar,
	})
	if err != nil {
		s.writeIdentityError(w, err)
		return
	}

	_ = s.Audit.RecordSync(ctx, audit.Event{
		Action: audit.ActionFarmerEnrolled, Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
		ResourceType: "user", ResourceID: user.ID,
		JurisdictionCode: rec.Farmer.VillageLGDCode,
		Detail: map[string]any{
			"farmer_id": farmerID,
			"mobile":    maskTail(mobile),
		},
	}.FromContext(ctx))

	writeJSON(w, http.StatusCreated, map[string]any{
		"user":         viewUser(user),
		"already":      false,
		"sign_in_hint": "the farmer signs in with a one-time code to " + maskTail(mobile),
	})
}
