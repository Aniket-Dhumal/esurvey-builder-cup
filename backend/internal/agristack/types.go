// Package agristack models the farmer, land and crop records the platform
// consumes, and provides sources for them.
//
// The field names mirror AgriStack's i1004:o1007 response exactly. That is
// deliberate: when the live geometry entitlement lands, the file-backed source
// is replaced by an HTTP one and nothing downstream changes.
package agristack

import (
	"context"
	"encoding/json"
)

// Geometry is a GeoJSON geometry, kept as raw coordinates so it can be handed
// to the satellite engine unaltered. AgriStack returns MultiPolygon; the
// satellite engine accepts Polygon or MultiPolygon.
type Geometry struct {
	Type        string          `json:"type"`
	Coordinates json.RawMessage `json:"coordinates"`
}

// Farmer is the demographic record. AgriStack returns this reliably.
type Farmer struct {
	FarmerID           string `json:"farmer_id"`
	FarmerName         string `json:"farmer_name"`
	Aadhaar            string `json:"aadhaar"`      // last four digits only
	AadhaarHash        string `json:"aadhaar_hash"` // SHA-256, the registry's own key
	DOB                string `json:"dob"`
	Gender             string `json:"gender"`
	MobileNo           string `json:"mobile_no"`
	Address            string `json:"address"`
	FarmerCategory     string `json:"farmer_category"`
	CasteCategory      string `json:"caste_category"`
	IdentifierType     string `json:"identifier_type"`
	IdentifierName     string `json:"identifier_name"`
	StateLGDCode       string `json:"state_lgd_code"`
	DistrictLGDCode    string `json:"district_lgd_code"`
	SubDistrictLGDCode string `json:"sub_district_lgd_code"`
	VillageLGDCode     string `json:"village_lgd_code"`
}

// JointOwner is a co-owner on the same khata.
type JointOwner struct {
	OwnerNumber     string `json:"owner_number"`
	MainOwnerNumber string `json:"main_owner_number"`
	OwnerNameROR    string `json:"owner_name_ror"`
	IdentifierName  string `json:"owner_identifier_name"`
}

// Parcel is one land record.
//
// Two area-like values live here and they are NOT interchangeable:
//
//	PlotGeometry is the boundary of the whole survey number.
//	OwnerExtent  is this owner's share of it.
//
// On the reference parcel the traced polygon is 4.82 ha while OwnerExtent is
// 1.6 ha, because the khata carries joint owners. Loss is assessed over the
// geometry and then apportioned by OwnerExtent; using polygon area as the
// insurable area would overpay every jointly-held parcel.
type Parcel struct {
	FarmerID           string       `json:"farmer_id"`
	FarmID             string       `json:"farm_id"`
	SurveyNumber       string       `json:"survey_number"`
	SubSurveyNumber    string       `json:"sub_survey_number"`
	OwnerNameROR       string       `json:"owner_name_ror"`
	VillageLGDCode     string       `json:"village_lgd_code"`
	DistrictLGDCode    string       `json:"district_lgd_code"`
	SubDistrictLGDCode string       `json:"sub_district_lgd_code"`
	PlotGeometry       *Geometry    `json:"plot_geometry"` // null on live AgriStack today
	ULPIN              *string      `json:"ulpin"`         // null on live AgriStack today
	OwnerExtent        string       `json:"owner_extent"`
	AreaUnit           string       `json:"area_unit"`
	KhataNumber        *string      `json:"khata_number"`
	JointOwners        []JointOwner `json:"joint_owners"`
}

// CropSown is one sown-crop declaration.
//
// AgriStack returns an empty array for every farmer tested, so in practice
// this is supplied by the mock or captured from the farmer at survey time.
//
// SownAreaInteger and SownAreaDecimal encode the area as two parts and the
// spec never states the decimal convention: it shows both {2, "30"} and
// {3, "00006"}. Area() reads it as integer.decimal; confirm against the real
// registry before any payout depends on it.
type CropSown struct {
	FarmID          string   `json:"farm_id"`
	SurveyNumber    string   `json:"survey_number"`
	VillageLGDCode  string   `json:"village_lgd_code"`
	StateLGDCode    string   `json:"state_lgd_code"`
	Year            string   `json:"year"`
	Season          string   `json:"season"`
	SownAreaInteger int      `json:"sown_area_integer"`
	SownAreaDecimal string   `json:"sown_area_decimal"`
	SownAreaUnit    string   `json:"sown_area_unit"`
	CropCode        string   `json:"crop_code"`
	CropName        string   `json:"crop_name"`
	SowingDate      string   `json:"sowing_date"` // DD/MM/YYYY
	IrrigationType  string   `json:"irrigation_type"`
	CropPhoto       []string `json:"crop_photo"`
}

// FarmerRecord is everything AgriStack returns for one farmer.
type FarmerRecord struct {
	StateLGDCode string     `json:"state_lgd_code"`
	Farmer       Farmer     `json:"FarmerData"`
	Parcels      []Parcel   `json:"land_data"`
	Crops        []CropSown `json:"crop_sown_data"`
}

// Source supplies farmer records. FileSource reads the mocks; an HTTP
// implementation against agristack.agribid.ai replaces it unchanged.
//
// Registry is the implementation the server runs on: stored records,
// refreshed from an Upstream (FileSource today, AgriStack when connected).
type Source interface {
	FarmerRecord(ctx context.Context, farmerID string) (*FarmerRecord, error)
}
