package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/agristack"
	"github.com/agribid/esurvey-platform/backend/internal/audit"
	"github.com/agribid/esurvey-platform/backend/internal/auth"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// myRegistryRecord serves a farmer their own AgriStack record.
//
// # Why this is not the officer route
//
// GET /api/v1/farmers/{farmerID} exists and is guarded by PermFarmerRead,
// which no farmer holds. Reusing it would mean either handing farmers an
// officer permission or loosening the officer route, and both end in the same
// place: any signed-in farmer able to read any farmer's record. The record
// carries an Aadhaar hash, a date of birth, a caste category, a khata number
// and the names of joint owners. A Farmer ID is printed on documents and known
// around a village, so "you need the ID" is not a control.
//
// # The rule
//
// A farmer may load a record two ways: it is the record whose registered
// mobile is the number they verified at sign-in, or they hold a grant for it
// (see farmer_lookup.go, and auth.IssueFarmerGrant).
//
// The first needs nothing guessed and nothing enumerated: the only record it
// reaches is the one that would have sent the code already answered. Sign-in
// stays mobile-and-OTP precisely so this holds.
//
// The second exists because AgriStack holds whatever number was given at
// enrolment, which is routinely one the farmer no longer has -- and the
// strict rule locks those farmers out of their own claims. It was decided on
// 2026-09-16 that they may instead answer for the record with its date of
// birth. That is a weaker proof than a SIM and is treated as one: the lookup
// that issues a grant counts failures and locks out, and this route only ever
// accepts the grant it produced, bound to the account it produced it for.
//
// # Why a wrong ID and an unknown ID answer identically
//
// Both are FARMER_NOT_FOUND with the same message. Distinguishing them would
// make the route an oracle for which Farmer IDs exist -- try a range, keep the
// ones that come back "not yours", and you have enumerated the registry
// without ever reading a record. The two branches below deliberately produce
// one answer.
func (s *Server) myRegistryRecord(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := rbac.FromContext(ctx)

	if !p.IsFarmer() {
		writeError(w, http.StatusForbidden, "FARMER_ONLY",
			"this route serves a farmer their own record; officers use /api/v1/farmers")
		return
	}

	farmerID := r.PathValue("farmerID")

	// A grant the handset is presenting for this farmer, if it has one. The
	// account it was issued to is checked inside FarmerGranted, so a grant
	// copied from another handset proves nothing here.
	granted := false
	if g := r.Header.Get("X-Farmer-Grant"); g != "" {
		if id, err := s.Auth.FarmerGranted(g, p.UserID); err == nil && id == farmerID {
			granted = true
		}
	}

	// The number this session actually proved. Read from the account rather
	// than taken from the token: a claim in a token is what the token says,
	// and the whole rule rests on this being what was verified.
	u, err := s.Identity.ByID(ctx, p.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "IDENTITY_UNAVAILABLE",
			"could not read the signed-in account")
		return
	}

	rec, recErr := s.Registry.FarmerRecord(ctx, farmerID)
	if errors.Is(recErr, agristack.ErrUnavailable) {
		writeRegistryUnavailable(w)
		return
	}

	// One answer for "no such farmer", "not your farmer" and "no grant".
	// See above.
	if recErr != nil || !(granted || sameMobile(rec.Farmer.MobileNo, u.MobileE164)) {
		s.Audit.Record(ctx, audit.Event{
			Action: audit.ActionFarmerRead, Outcome: audit.OutcomeDeny,
			ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
			ResourceType: "farmer", ResourceID: farmerID,
		}.FromContext(ctx))

		writeError(w, http.StatusNotFound, "FARMER_NOT_FOUND",
			"no record for that farmer id")
		return
	}

	s.Audit.Record(ctx, audit.Event{
		Action: audit.ActionFarmerRead, Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
		ResourceType: "farmer", ResourceID: farmerID,
		JurisdictionCode: rec.Farmer.VillageLGDCode,
	}.FromContext(ctx))

	writeJSON(w, http.StatusOK, projectForFarmer(rec))
}

// sameMobile reports whether two numbers are the same subscriber.
//
// Compared after normalisation because the two sides disagree by convention
// rather than by value: AgriStack carries a bare ten-digit number and the
// account holds E.164. A raw string comparison would refuse every farmer.
//
// An empty number on either side never matches. Absent is not a value, and a
// blank-equals-blank match would hand any account with no mobile the record of
// any farmer with no mobile.
func sameMobile(registry, account string) bool {
	if registry == "" || account == "" {
		return false
	}
	a, err := auth.NormalizeMobile(registry)
	if err != nil {
		return false
	}
	b, err := auth.NormalizeMobile(account)
	if err != nil {
		return false
	}
	return a == b
}

// --- the projection ---------------------------------------------------------

// farmerRecordView is what a handset is given.
//
// A projection rather than the record. Everything here is drawn on a farmer
// screen; everything left out is not. The omissions are the point: aadhaar,
// aadhaar_hash, dob, gender, caste_category, khata_number and joint_owners are
// all in the record and none of them reach the phone. These handsets are
// shared and borrowed, and a caste category cached on one is a liability with
// no matching use.
type farmerRecordView struct {
	FarmerID   string `json:"farmer_id"`
	FarmerName string `json:"farmer_name"`
	Address    string `json:"address"`

	StateLGDCode       string `json:"state_lgd_code"`
	DistrictLGDCode    string `json:"district_lgd_code"`
	SubDistrictLGDCode string `json:"sub_district_lgd_code"`
	VillageLGDCode     string `json:"village_lgd_code"`

	Parcels []farmerParcelView `json:"parcels"`

	// FarmerGrant is proof that this account answered for this farmer, which
	// the handset keeps beside them and presents to refresh the record. Set
	// only by the lookup that issues it; a refresh returns the record alone,
	// because the caller already holds the grant they arrived with.
	//
	// It is not a credential for anything else: see auth.IssueFarmerGrant.
	FarmerGrant    string    `json:"farmer_grant,omitempty"`
	GrantExpiresAt time.Time `json:"grant_expires_at,omitempty"`
}

type farmerParcelView struct {
	FarmID          string `json:"farm_id"`
	SurveyNumber    string `json:"survey_number"`
	SubSurveyNumber string `json:"sub_survey_number"`

	// Where the parcel is. A farmer's parcels can lie in more than one
	// village, and a survey event covers villages rather than farmers -- so
	// this is what the handset asks for events by. Without it the handset had
	// only a district to filter on and offered events that did not cover the
	// farmer's land.
	VillageLGDCode string `json:"village_lgd_code"`

	// The name as the record of rights spells it, which is Devanagari. Shown
	// in preference to the transliterated FarmerName: a farmer confirming
	// their own parcel should see the name they would see on the 7/12.
	OwnerNameROR string `json:"owner_name_ror"`

	// This owner's share, not the polygon's area. The two differ on every
	// jointly-held parcel and confusing them overpays.
	OwnerExtent string `json:"owner_extent"`
	AreaUnit    string `json:"area_unit"`

	// Null on live AgriStack, which is why the app has a boundary-tracing
	// screen. Its absence is what sends a farmer there.
	PlotGeometry *agristack.Geometry `json:"plot_geometry"`

	Crops []farmerCropView `json:"crops"`
}

type farmerCropView struct {
	CropName       string  `json:"crop_name"`
	Season         string  `json:"season"`
	Year           string  `json:"year"`
	SowingDate     string  `json:"sowing_date"`
	IrrigationType string  `json:"irrigation_type"`
	SownAreaHa     float64 `json:"sown_area_ha,omitempty"`
}

// projectForFarmer reduces a registry record to what the app draws.
//
// Crops are attached to the parcel they were sown on rather than left in a
// flat list beside it. The app asks "what is on this survey number" and the
// registry answers with two arrays joined by farm_id; doing that join here
// means it is done once, correctly, instead of in every screen that needs it.
func projectForFarmer(rec *agristack.FarmerRecord) farmerRecordView {
	out := farmerRecordView{
		FarmerID:           rec.Farmer.FarmerID,
		FarmerName:         rec.Farmer.FarmerName,
		Address:            rec.Farmer.Address,
		StateLGDCode:       rec.Farmer.StateLGDCode,
		DistrictLGDCode:    rec.Farmer.DistrictLGDCode,
		SubDistrictLGDCode: rec.Farmer.SubDistrictLGDCode,
		VillageLGDCode:     rec.Farmer.VillageLGDCode,
		Parcels:            []farmerParcelView{},
	}

	for _, parcel := range rec.Parcels {
		view := farmerParcelView{
			FarmID:          parcel.FarmID,
			SurveyNumber:    parcel.SurveyNumber,
			SubSurveyNumber: parcel.SubSurveyNumber,
			VillageLGDCode:  parcel.VillageLGDCode,
			OwnerNameROR:    parcel.OwnerNameROR,
			OwnerExtent:     parcel.OwnerExtent,
			AreaUnit:        parcel.AreaUnit,
			PlotGeometry:    parcel.PlotGeometry,
			Crops:           []farmerCropView{},
		}

		for i := range rec.Crops {
			crop := &rec.Crops[i]
			if crop.FarmID != parcel.FarmID {
				continue
			}
			c := farmerCropView{
				CropName:       crop.CropName,
				Season:         crop.Season,
				Year:           crop.Year,
				SowingDate:     crop.SowingDate,
				IrrigationType: crop.IrrigationType,
			}
			// A sown area the registry encodes ambiguously is better omitted
			// than guessed at: the field is optional on the handset and a
			// wrong hectarage pre-filled into a claim is worse than a blank
			// one the farmer fills in.
			if ha, err := crop.Area(); err == nil {
				c.SownAreaHa = ha
			}
			view.Crops = append(view.Crops, c)
		}

		out.Parcels = append(out.Parcels, view)
	}

	return out
}
