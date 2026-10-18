package api

import (
	"net/http/httptest"
	"testing"
)

// A carrier gateway's phones refreshing together must not share the budget
// meant for an officer's desk.
func TestFarmerAuthRoutesHaveTheirOwnLargerBudget(t *testing.T) {
	s := &Server{}
	for path := range farmerAuthRoutes {
		key, budget := s.rateKey(httptest.NewRequest("POST", path, nil))
		if budget != rateBurstFarmerAuth || key[:5] != "rl:f:" {
			t.Errorf("%s: key %q budget %d; want the farmer-auth bucket", path, key, budget)
		}
	}
	key, budget := s.rateKey(httptest.NewRequest("POST", "/api/v1/auth/officer/login", nil))
	if budget != rateBurstAnonymous || key[:5] != "rl:a:" {
		t.Errorf("officer login: key %q budget %d; want the anonymous bucket", key, budget)
	}
}
