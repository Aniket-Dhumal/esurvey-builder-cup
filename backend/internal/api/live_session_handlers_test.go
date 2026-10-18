package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"testing"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/agristack"
	"github.com/agribid/esurvey-platform/backend/internal/identity"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// What the relay is told about where the claim is.
//
// The thing being pinned down here is that a district reaching the model is a
// district somebody could look up -- never an LGD code wearing a name's label,
// and never a name the platform did not have. The second half is that none of
// it is load-bearing: every way the lookup can fail has to end with a session
// the farmer can still open.

// districtDirectory is the jurisdiction reference data, as much of it as a test
// has a use for.
type districtDirectory map[string]string

func (d districtDirectory) JurisdictionByCode(
	_ context.Context, level rbac.Level, code string,
) (*identity.Jurisdiction, error) {
	if level != rbac.LevelDistrict {
		// Worth failing on rather than answering: a lookup at the wrong level
		// would find a taluka of the same code and call it a district.
		return nil, fmt.Errorf("asked for a %s where a district was meant", level)
	}
	name, ok := d[code]
	if !ok {
		return nil, identity.ErrNotFound
	}
	return &identity.Jurisdiction{Level: level, Code: code, Name: name}, nil
}

// maharashtra is the real codes and names for the two districts this file uses.
var maharashtra = districtDirectory{"521": "पुणे", "525": "सोलापूर"}

// brokenDirectory is the reference table unreachable.
type brokenDirectory struct{}

func (brokenDirectory) JurisdictionByCode(
	context.Context, rbac.Level, string,
) (*identity.Jurisdiction, error) {
	return nil, errors.New("dial tcp: connection refused")
}

// slowDirectory is the reference table answering eventually, which on this path
// is the same as not answering.
type slowDirectory struct{}

func (slowDirectory) JurisdictionByCode(
	ctx context.Context, _ rbac.Level, _ string,
) (*identity.Jurisdiction, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// farmerFrom is a registry holding one farmer, in the district given.
type farmerFrom string

func (d farmerFrom) FarmerRecord(
	_ context.Context, farmerID string,
) (*agristack.FarmerRecord, error) {
	return &agristack.FarmerRecord{Farmer: agristack.Farmer{
		FarmerID: farmerID, DistrictLGDCode: string(d),
	}}, nil
}

// unreachableRegistry is AgriStack down, which is a daily occurrence.
type unreachableRegistry struct{}

func (unreachableRegistry) FarmerRecord(
	context.Context, string,
) (*agristack.FarmerRecord, error) {
	return nil, agristack.ErrUnavailable
}

func liveServer(districts districtNames, registry agristack.Source) *Server {
	return &Server{
		LiveRelayURL:     "https://esurvey.example/api/v1/live/ws",
		LiveModel:        "gemini-live",
		LiveTicketSecret: ticketSecret,
		Districts:        districts,
		Registry:         registry,
		Log:              slog.New(slog.DiscardHandler),
	}
}

// districtOnTheRelayURL is what the relay would read, taken off the URL the
// handset is handed rather than from the resolver's return value -- the query
// string is the only part of this the model ever sees.
func districtOnTheRelayURL(
	t *testing.T, s *Server, farmerID, sentCode, sentName string,
) string {
	t.Helper()
	u, err := url.Parse(s.liveURL(
		"cotton", "hail", "kharif",
		s.liveDistrict(context.Background(), farmerID, sentCode, sentName),
		liveTicket(s.LiveTicketSecret, 480, time.Now()),
	))
	if err != nil {
		t.Fatalf("the granted URL does not parse: %v", err)
	}
	if u.Query().Get("ticket") == "" {
		t.Fatal("the URL carries no ticket, so the relay would refuse it")
	}
	return u.Query().Get("district")
}

func TestALiveSessionCarriesTheDistrictNameRatherThanItsCode(t *testing.T) {
	s := liveServer(maharashtra, farmerFrom("525"))

	if got := districtOnTheRelayURL(t, s, "F-1", "", ""); got != "सोलापूर" {
		t.Fatalf("district %q, want the name the code resolves to", got)
	}
}

func TestALiveSessionsDistrictComesFromTheFarmersRecordNotTheHandset(t *testing.T) {
	// The handset holds an LGD code and nothing that turns it into a place, so
	// a name from it is a guess. Here it guesses a real district that is the
	// wrong one, and sends a code for a third -- the record still wins.
	s := liveServer(maharashtra, farmerFrom("525"))

	if got := districtOnTheRelayURL(t, s, "F-1", "521", "Nagpur"); got != "सोलापूर" {
		t.Fatalf("district %q, want the farmer's own district", got)
	}
}

func TestALiveSessionUsesTheCodeAHandsetSentOnlyWhenTheRecordHasNone(t *testing.T) {
	// A registry record with no district has told us nothing, rather than told
	// us there is nothing, so the handset is worth asking at that point.
	s := liveServer(maharashtra, farmerFrom(""))

	if got := districtOnTheRelayURL(t, s, "F-1", "521", ""); got != "पुणे" {
		t.Fatalf("district %q, want the district the sent code resolves to", got)
	}
}

func TestAnUnknownDistrictCodeLeavesALiveSessionWithNoDistrict(t *testing.T) {
	// The relay's prompt works without a district, and inventing a name for a
	// code nobody has recorded is the one thing that must not happen.
	s := liveServer(maharashtra, farmerFrom("999"))

	if got := districtOnTheRelayURL(t, s, "F-1", "998", ""); got != "" {
		t.Fatalf("district %q, want nothing for a code the platform does not hold", got)
	}
}

func TestALiveSessionNeverSendsNothingButDigitsAsADistrictName(t *testing.T) {
	// The whole point of the filter: a caller cannot get a code to the model by
	// putting it in the field labelled with a name.
	s := liveServer(maharashtra, farmerFrom(""))

	for _, sent := range []string{"525", "0", "27"} {
		if got := districtOnTheRelayURL(t, s, "F-1", "", sent); got != "" {
			t.Errorf("free-text %q arrived as district %q", sent, got)
		}
	}
}

func TestALiveSessionPassesThroughARealDistrictNameFromAHandset(t *testing.T) {
	// Nothing resolves, and what is left cannot be a code, so it is what it
	// says it is.
	s := liveServer(maharashtra, farmerFrom(""))

	if got := districtOnTheRelayURL(t, s, "F-1", "", "Solapur"); got != "Solapur" {
		t.Fatalf("district %q, want the name as sent", got)
	}
}

func TestAFailedDistrictLookupStillLeavesALiveSessionToOpen(t *testing.T) {
	// Both sources of the answer broken at once. The farmer is standing in a
	// field and the assistant is the thing that must survive, not the district.
	s := liveServer(brokenDirectory{}, unreachableRegistry{})

	if got := districtOnTheRelayURL(t, s, "F-1", "525", "Solapur"); got != "Solapur" {
		t.Fatalf("district %q, want the session granted with what was sendable", got)
	}

	// And with nothing sendable either, still a URL with a ticket on it, which
	// is what districtOnTheRelayURL asserts on the way past.
	if got := districtOnTheRelayURL(t, s, "F-1", "525", ""); got != "" {
		t.Fatalf("district %q, want nothing", got)
	}
}

func TestASlowReferenceTableDoesNotHoldUpALiveSession(t *testing.T) {
	s := liveServer(slowDirectory{}, farmerFrom("525"))

	start := time.Now()
	got := districtOnTheRelayURL(t, s, "F-1", "", "")
	if got != "" {
		t.Errorf("district %q, want nothing from a table that never answered", got)
	}
	if waited := time.Since(start); waited > 4*liveDistrictBudget {
		t.Fatalf("waited %s on the reference table, budget is %s",
			waited, liveDistrictBudget)
	}
}

func TestALiveSessionSendsNoDistrictWhereNothingKnowsOfOne(t *testing.T) {
	// A deployment with no reference data wired at all, which is every test
	// server and some early installations.
	s := liveServer(nil, nil)

	if got := districtOnTheRelayURL(t, s, "F-1", "525", ""); got != "" {
		t.Fatalf("district %q, want nothing", got)
	}
}
