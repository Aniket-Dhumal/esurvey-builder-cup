package agristack

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The stored registry: what a farmer gets when AgriStack is slow, down, or
// asked the same question fifty times, and whose records a listing reaches.

func registryDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("ESURVEY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set ESURVEY_TEST_DATABASE_URL to run the registry store tests")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Skipf("no database: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Skipf("no database: %v", err)
	}
	t.Cleanup(pool.Close)

	clean := func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM registry_farmers WHERE farmer_id LIKE 'RT-%'`)
		_, _ = pool.Exec(c, `DELETE FROM jurisdictions WHERE code LIKE 'RT-%' AND level = 'village'`)
		_, _ = pool.Exec(c, `DELETE FROM jurisdictions WHERE code LIKE 'RT-%' AND level = 'sub_district'`)
		_, _ = pool.Exec(c, `DELETE FROM jurisdictions WHERE code LIKE 'RT-%' AND level = 'district'`)
		_, _ = pool.Exec(c, `DELETE FROM jurisdictions WHERE code LIKE 'RT-%' AND level = 'state'`)
	}
	clean()
	t.Cleanup(clean)
	return pool
}

// fakeUpstream is a registry whose answers the test controls.
type fakeUpstream struct {
	mu      sync.Mutex
	records map[string]*FarmerRecord
	down    bool
	delay   time.Duration
	calls   atomic.Int64
}

func (f *fakeUpstream) Name() string { return "fake" }

func (f *fakeUpstream) Fetch(ctx context.Context, id string) (*FarmerRecord, error) {
	f.calls.Add(1)
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return nil, errors.New("connection refused")
	}
	rec, ok := f.records[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *rec
	return &cp, nil
}

func record(id, name, village string, parcels ...Parcel) *FarmerRecord {
	return &FarmerRecord{
		StateLGDCode: "27",
		Farmer: Farmer{FarmerID: id, FarmerName: name, VillageLGDCode: village,
			DistrictLGDCode: "RT-D", MobileNo: "9876500000", Aadhaar: "4138"},
		Parcels: parcels,
	}
}

func parcel(farm, village string, mapped bool) Parcel {
	p := Parcel{FarmID: farm, SurveyNumber: strings.TrimPrefix(farm, "RT-FARM-"),
		VillageLGDCode: village, OwnerExtent: "1.6", AreaUnit: "Hectare"}
	if mapped {
		p.PlotGeometry = &Geometry{Type: "Polygon",
			Coordinates: json.RawMessage(`[[[75.81,18.17],[75.82,18.17],[75.82,18.18],[75.81,18.17]]]`)}
	}
	return p
}

func TestAStoredRecordIsServedWhileTheRegistryIsDown(t *testing.T) {
	pool := registryDB(t)
	ctx := context.Background()
	up := &fakeUpstream{records: map[string]*FarmerRecord{
		"RT-1": record("RT-1", "Harichandra", "RT-V1"),
	}}
	reg := NewRegistry(pool, up, nil)

	if _, err := reg.FarmerRecord(ctx, "RT-1"); err != nil {
		t.Fatalf("first read: %v", err)
	}
	// A day passes and AgriStack goes down.
	if _, err := pool.Exec(ctx, `UPDATE registry_farmers
		SET fetched_at = now() - interval '2 days' WHERE farmer_id = 'RT-1'`); err != nil {
		t.Fatal(err)
	}
	up.down = true

	rec, err := reg.FarmerRecord(ctx, "RT-1")
	if err != nil {
		t.Fatalf("a farmer the platform already knows was refused during an outage: %v", err)
	}
	if rec.Farmer.FarmerName != "Harichandra" {
		t.Errorf("served %q", rec.Farmer.FarmerName)
	}
	if reg.Stats(ctx).ServedStale != 1 {
		t.Error("serving a stale record was not counted")
	}
}

func TestAnUnknownFarmerDuringAnOutageIsUnavailableNotMissing(t *testing.T) {
	// "No such farmer" to someone who exists is the wrong thing to say, and
	// the lookup lockout counts it as a wrong guess.
	pool := registryDB(t)
	up := &fakeUpstream{down: true}
	reg := NewRegistry(pool, up, nil)

	_, err := reg.FarmerRecord(context.Background(), "RT-NEVER-SEEN")
	if !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v; want ErrUnavailable", err)
	}
}

func TestAnIdTheRegistryDoesNotHoldIsNotAskedAboutAgainAtOnce(t *testing.T) {
	pool := registryDB(t)
	up := &fakeUpstream{records: map[string]*FarmerRecord{}}
	reg := NewRegistry(pool, up, nil)
	ctx := context.Background()

	for range 5 {
		if _, err := reg.FarmerRecord(ctx, "RT-TYPO"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v; want ErrNotFound", err)
		}
	}
	if n := up.calls.Load(); n != 1 {
		t.Errorf("the registry was asked %d times about an id it said it does not hold", n)
	}
}

func TestFiftyRequestsForOneFarmerAreOneRegistryCall(t *testing.T) {
	pool := registryDB(t)
	up := &fakeUpstream{delay: 200 * time.Millisecond, records: map[string]*FarmerRecord{
		"RT-2": record("RT-2", "Sunita", "RT-V1"),
	}}
	reg := NewRegistry(pool, up, nil)

	var wg sync.WaitGroup
	var failed atomic.Int64
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := reg.FarmerRecord(context.Background(), "RT-2"); err != nil {
				failed.Add(1)
			}
		}()
	}
	wg.Wait()
	if failed.Load() > 0 {
		t.Fatalf("%d requests failed", failed.Load())
	}
	if n := up.calls.Load(); n != 1 {
		t.Errorf("%d registry calls for one farmer", n)
	}
}

func TestARegistryThatKeepsFailingStopsBeingCalled(t *testing.T) {
	pool := registryDB(t)
	up := &fakeUpstream{down: true}
	reg := NewRegistry(pool, up, nil)
	ctx := context.Background()

	for i := range registryBreakerThreshold {
		_, _ = reg.FarmerRecord(ctx, "RT-B"+string(rune('a'+i)))
	}
	before := up.calls.Load()
	began := time.Now()
	_, err := reg.FarmerRecord(ctx, "RT-Bz")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if up.calls.Load() != before {
		t.Error("the registry was called with the circuit open")
	}
	if time.Since(began) > 100*time.Millisecond {
		t.Errorf("an open circuit still took %s to answer", time.Since(began))
	}
	if !reg.Stats(ctx).CircuitOpen {
		t.Error("stats do not show the circuit open")
	}
}

func TestASlowRegistryIsGivenUpOnNotWaitedFor(t *testing.T) {
	pool := registryDB(t)
	up := &fakeUpstream{delay: time.Hour, records: map[string]*FarmerRecord{}}
	reg := NewRegistry(pool, up, nil)
	reg.CallTimeout = 100 * time.Millisecond

	began := time.Now()
	_, err := reg.FarmerRecord(context.Background(), "RT-SLOW")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if took := time.Since(began); took > 3*time.Second {
		t.Errorf("waited %s for a hung registry", took)
	}
}

func TestAFullAadhaarNumberIsNeverStored(t *testing.T) {
	pool := registryDB(t)
	ctx := context.Background()
	reg := NewRegistry(pool, nil, nil)

	rec := record("RT-3", "Ramesh", "RT-V1")
	rec.Farmer.Aadhaar = "123456784138"
	if err := reg.Put(ctx, rec, "fake"); err != nil {
		t.Fatal(err)
	}
	var col, inRecord string
	if err := pool.QueryRow(ctx, `SELECT aadhaar_last4, record #>> '{FarmerData,aadhaar}'
		FROM registry_farmers WHERE farmer_id = 'RT-3'`).Scan(&col, &inRecord); err != nil {
		t.Fatal(err)
	}
	if col != "4138" || inRecord != "4138" {
		t.Fatalf("stored %q and %q; only the last four digits may be kept", col, inRecord)
	}

	// And the table refuses it from any other writer.
	_, err := pool.Exec(ctx, `UPDATE registry_farmers
		SET record = jsonb_set(record, '{FarmerData,aadhaar}', '"123456784138"')
		WHERE farmer_id = 'RT-3'`)
	if err == nil {
		t.Fatal("the table accepted a full Aadhaar number")
	}
}

func TestAnUnchangedRecordIsNotRewritten(t *testing.T) {
	pool := registryDB(t)
	ctx := context.Background()
	reg := NewRegistry(pool, nil, nil)

	var calls int
	reg.OnRecord = func(context.Context, *FarmerRecord) { calls++ }
	rec := record("RT-4", "Meena", "RT-V1", parcel("RT-FARM-41", "RT-V1", true))
	for range 3 {
		if err := reg.Put(ctx, rec, "fake"); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Errorf("OnRecord ran %d times for one record stored three times", calls)
	}
}

// seedPlaces makes a state with two villages in different talukas.
func seedPlaces(t *testing.T, pool *pgxpool.Pool) map[string]string {
	t.Helper()
	ids := map[string]string{}
	for _, j := range []struct{ level, code, parent string }{
		{"state", "RT-S", ""},
		{"district", "RT-D", "RT-S"},
		{"sub_district", "RT-T1", "RT-D"},
		{"sub_district", "RT-T2", "RT-D"},
		{"village", "RT-V1", "RT-T1"},
		{"village", "RT-V2", "RT-T2"},
	} {
		var parent any
		if j.parent != "" {
			parent = ids[j.parent]
		}
		var id string
		if err := pool.QueryRow(context.Background(), `
			INSERT INTO jurisdictions (level, code, name, parent_id)
			VALUES ($1::jurisdiction_level, $2, $2, $3::uuid) RETURNING id`,
			j.level, j.code, parent).Scan(&id); err != nil {
			t.Fatalf("seeding %s: %v", j.code, err)
		}
		ids[j.code] = id
	}
	return ids
}

func TestListingsReachOnlyTheCallersVillages(t *testing.T) {
	pool := registryDB(t)
	ctx := context.Background()
	places := seedPlaces(t, pool)
	reg := NewRegistry(pool, nil, nil)

	for _, rec := range []*FarmerRecord{
		record("RT-10", "Anil", "RT-V1", parcel("RT-FARM-101", "RT-V1", true)),
		record("RT-11", "Bharati 50%_off", "RT-V1", parcel("RT-FARM-111", "RT-V1", false)),
		record("RT-12", "Chandra", "RT-V2", parcel("RT-FARM-121", "RT-V2", true)),
	} {
		if err := reg.Put(ctx, rec, "fake"); err != nil {
			t.Fatal(err)
		}
	}

	taluka1 := Reach{JurisdictionIDs: []string{places["RT-T1"]}}
	rows, total, err := reg.Farmers(ctx, FarmerQuery{Reach: taluka1, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("taluka 1 reached %d farmers; want its own 2", total)
	}
	for _, r := range rows {
		if r.VillageCode != "RT-V1" {
			t.Errorf("taluka 1 reached %s in %s", r.FarmerID, r.VillageCode)
		}
	}

	district := Reach{JurisdictionIDs: []string{places["RT-D"]}}
	if _, total, _ := reg.Farmers(ctx, FarmerQuery{Reach: district, Limit: 50}); total != 3 {
		t.Errorf("the district reached %d farmers; want 3", total)
	}
	if rows, total, _ := reg.Farmers(ctx, FarmerQuery{Reach: Reach{}, Limit: 50}); total != 0 || len(rows) != 0 {
		t.Errorf("an empty reach reached %d farmers; it must reach none", total)
	}
	if _, total, _ := reg.Farmers(ctx, FarmerQuery{Reach: Reach{FarmerID: "RT-12"}, Limit: 50}); total != 1 {
		t.Errorf("a farmer reached %d records; want their own", total)
	}

	// Search is literal: a % in the query is a character, not a wildcard.
	if _, total, _ := reg.Farmers(ctx, FarmerQuery{Reach: district, Search: "50%_", Limit: 50}); total != 1 {
		t.Errorf("searching a literal %%_ found %d", total)
	}
	if _, total, _ := reg.Farmers(ctx, FarmerQuery{Reach: district, Search: "%", Limit: 50}); total != 1 {
		t.Errorf("a bare %% matched %d farmers; it is not a wildcard", total)
	}

	// The filters the console sends.
	if _, total, _ := reg.Farmers(ctx, FarmerQuery{Reach: district, Boundary: BoundaryMissing, Limit: 50}); total != 1 {
		t.Errorf("farmers missing a boundary: %d, want 1", total)
	}
	if _, total, _ := reg.Farmers(ctx, FarmerQuery{Reach: district, Boundary: BoundaryComplete, Limit: 50}); total != 2 {
		t.Errorf("farmers fully mapped: %d, want 2", total)
	}
	if _, total, _ := reg.Parcels(ctx, ParcelQuery{Reach: district, Boundary: BoundaryWithout, Limit: 50}); total != 1 {
		t.Errorf("parcels without a boundary: %d, want 1", total)
	}
	if _, total, _ := reg.Parcels(ctx, ParcelQuery{Reach: taluka1, Crop: CropUndeclared, Limit: 50}); total != 2 {
		t.Errorf("parcels with no crop in taluka 1: %d, want 2", total)
	}

	// Pages: the total is the whole match, the rows are the page.
	rows, total, _ = reg.Farmers(ctx, FarmerQuery{Reach: district, Limit: 2, Offset: 2})
	if total != 3 || len(rows) != 1 || rows[0].FarmerName != "Chandra" {
		t.Errorf("page 2 of 2: total %d, %d rows %+v", total, len(rows), rows)
	}

	tot, err := reg.Totals(ctx, district)
	if err != nil || tot.Farmers != 3 || tot.Parcels != 3 || tot.WithBoundary != 2 {
		t.Errorf("totals %+v (%v)", tot, err)
	}
}
