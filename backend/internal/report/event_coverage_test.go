package report

import (
	"context"
	"testing"

	"github.com/agribid/esurvey-platform/backend/internal/event"
	"github.com/agribid/esurvey-platform/backend/internal/platform/page"
)

// The events a farmer is offered, and whether they can file against them.
//
// Seen on the first real run: the handset filtered events by district, which
// cannot see the talukas and circles an event actually covers. It offered an
// event in the farmer's district that did not cover their village, and the
// claim was filled in, walked and photographed before the office refused it:
// "This plot is outside the survey area."
//
// Here in the report package because the fixture that builds a village inside
// a covered taluka is here, and because the rule being matched is the one a
// filing is refused by.

func TestEventsAreOfferedOnlyForVillagesTheyCover(t *testing.T) {
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	// newFiling covers the fixture taluka, which contains RV-V1.
	newFiling(t, pool, "cover-000000001")
	events := event.NewStore(pool)
	ctx := context.Background()

	covered, err := events.List(ctx, event.ListFilter{
		Villages: []string{"RV-V1"},
		Page:     page.Params{Limit: 50},
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range covered.Items {
		if e.Reference == "RV-EVT-1" {
			found = true
		}
	}
	if !found {
		t.Error("an event covering the farmer's village was not offered")
	}

	elsewhere, err := events.List(ctx, event.ListFilter{
		Villages: []string{"NOT-A-VILLAGE"},
		Page:     page.Params{Limit: 50},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range elsewhere.Items {
		if e.Reference == "RV-EVT-1" {
			t.Error("an event was offered for a village it does not cover; the " +
				"claim would be gathered in full and refused on arrival")
		}
	}
	if elsewhere.Total != len(elsewhere.Items) && elsewhere.Total >= 0 {
		t.Errorf("total %d disagrees with %d items: the count must apply the "+
			"same filter as the page", elsewhere.Total, len(elsewhere.Items))
	}
}

func TestNoVillagesMeansNoVillageFilter(t *testing.T) {
	// An officer's listing names no villages and must be unchanged.
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	events := event.NewStore(pool)

	all, err := events.List(context.Background(), event.ListFilter{
		Page: page.Params{Limit: 50},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Items) == 0 {
		t.Error("with no villages named, nothing was listed")
	}
}
