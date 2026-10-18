package evidence

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/agribid/esurvey-platform/backend/internal/satellite"
)

type fakeMoisture struct {
	res   *satellite.MoistureResult
	err   error
	calls atomic.Int32
	last  satellite.MoistureRequest
	mu    sync.Mutex
}

func (f *fakeMoisture) Moisture(_ context.Context, req satellite.MoistureRequest) (*satellite.MoistureResult, error) {
	f.calls.Add(1)
	f.mu.Lock()
	f.last = req
	f.mu.Unlock()
	return f.res, f.err
}

func fp(v float64) *float64 { return &v }

// dryField is a measurement of a field drier than its history and its
// neighbours near an incident on 14 August.
func dryField(missions ...string) *satellite.MoistureResult {
	pass := func(date string, offset int, ndmi, ring float64) satellite.OpticalPass {
		return satellite.OpticalPass{
			Date: date, SeasonOffset: offset, NDMI: fp(ndmi), NDVI: fp(0.5),
			ClearFraction: 1, ClearPixels: 40,
			RingNDMI: fp(ring), RingClearPixels: 8000,
		}
	}
	if len(missions) == 0 {
		missions = []string{"SENTINEL-2", "SENTINEL-1", "ESA-WORLDCOVER"}
	}
	return &satellite.MoistureResult{
		Optical: []satellite.OpticalPass{
			pass("2025-08-12", 1, 0.33, 0.31),
			pass("2024-08-16", 2, 0.34, 0.32),
			pass("2023-08-10", 3, 0.32, 0.30),
			pass("2026-07-10", 0, 0.32, 0.31),
			pass("2026-08-10", 0, 0.20, 0.30),
			pass("2026-08-20", 0, 0.19, 0.29),
		},
		ParcelPixels: 40, RingCroplandPixels: 18000,
		BaselineYears: 3, Missions: missions,
	}
}

func droughtInput() Input {
	in := input()
	in.CalamityType = "drought"
	return in
}

func TestAMoistureReadingReachesThePacket(t *testing.T) {
	src := &fakeMoisture{res: dryField()}
	a := &Assembler{Log: quiet(), Moisture: src}

	p := a.Assemble(context.Background(), droughtInput(), nil)
	if p.Moisture == nil {
		t.Fatalf("no moisture reading: %v", p.Absent)
	}
	if len(p.Moisture.Findings) == 0 {
		t.Fatal("no findings")
	}
	if !strings.HasPrefix(p.Moisture.Relevance, "Strong") {
		t.Errorf("relevance for a drought = %q", p.Moisture.Relevance)
	}

	// What was asked for: three prior seasons, over a window that starts no
	// earlier than sowing.
	if src.last.BaselineYears != MoistureBaselineYears {
		t.Errorf("baseline years = %d", src.last.BaselineYears)
	}
	if src.last.StartDate != "2026-06-15" {
		t.Errorf("window starts %s, want the sowing date", src.last.StartDate)
	}
}

// Moisture corroborates. It must never be what grounds a figure on its own,
// because nothing in it measures the loss.
func TestAMoistureReadingAloneDoesNotGroundTheFigure(t *testing.T) {
	a := &Assembler{Log: quiet(), Moisture: &fakeMoisture{res: dryField()}}
	p := a.Assemble(context.Background(), droughtInput(), nil)
	if p.Moisture == nil {
		t.Fatalf("no moisture reading: %v", p.Absent)
	}
	if p.Grounded() {
		t.Fatal("a moisture reading alone grounded the damage figure")
	}
}

func TestAServiceWithoutTheModuleIsReportedNotFatal(t *testing.T) {
	a := &Assembler{Log: quiet(), Moisture: &fakeMoisture{err: satellite.ErrMoistureUnavailable}}
	p := a.Assemble(context.Background(), droughtInput(), nil)
	if p == nil {
		t.Fatal("no packet")
	}
	if !absentMentions(p, "moisture") {
		t.Errorf("a missing moisture module was not reported: %v", p.Absent)
	}
}

func TestAGeneratedMoistureRecordIsRefusedOutsideDevelopment(t *testing.T) {
	a := &Assembler{Log: quiet(), Moisture: &fakeMoisture{res: dryField("SYNTHETIC")}}
	p := a.Assemble(context.Background(), droughtInput(), nil)
	if p.Moisture != nil {
		t.Fatal("a generated moisture record was accepted as evidence")
	}
	if !absentMentions(p, "generated") {
		t.Errorf("the refusal did not say why: %v", p.Absent)
	}

	a.AllowSynthetic = true
	p = a.Assemble(context.Background(), droughtInput(), nil)
	if p.Moisture == nil || !p.Moisture.Synthetic {
		t.Fatal("in development a generated record should be accepted and labelled")
	}
}

func TestAParcelWithNoBoundaryIsNotSentToTheService(t *testing.T) {
	src := &fakeMoisture{res: dryField()}
	a := &Assembler{Log: quiet(), Moisture: src}
	in := droughtInput()
	in.Geometry = satellite.Geometry{}

	p := a.Assemble(context.Background(), in, nil)
	if src.calls.Load() != 0 {
		t.Fatal("a parcel with no boundary was sent to Earth Engine")
	}
	if !absentMentions(p, "boundary") {
		t.Errorf("the absence does not name the boundary: %v", p.Absent)
	}
}

// --- the cache ------------------------------------------------------------

type mapCache struct {
	mu        sync.Mutex
	rows      map[string][]byte
	permanent map[string]bool
}

func newMapCache() *mapCache {
	return &mapCache{rows: map[string][]byte{}, permanent: map[string]bool{}}
}

func (m *mapCache) k(product, key, start, end string) string {
	return product + "|" + key + "|" + start + "|" + end
}

func (m *mapCache) Get(_ context.Context, product, key, start, end string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	raw, ok := m.rows[m.k(product, key, start, end)]
	return raw, ok
}

func (m *mapCache) Put(_ context.Context, product, key, start, end string, raw []byte, permanent bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[m.k(product, key, start, end)] = raw
	m.permanent[m.k(product, key, start, end)] = permanent
}

func (m *mapCache) Forget(_ context.Context, product, key, start, end string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, m.k(product, key, start, end))
}

func request(coords string) satellite.MoistureRequest {
	return satellite.MoistureRequest{
		Geometry:  satellite.Geometry{Type: "Polygon", Coordinates: json.RawMessage(coords)},
		StartDate: "2025-06-15", EndDate: "2025-09-13", BaselineYears: 3,
	}
}

// A settled window is bought once. A re-assessment, an appeal and the printed
// dossier all ask the same question of the same archive.
func TestASettledWindowIsMeasuredOnce(t *testing.T) {
	src := &fakeMoisture{res: dryField()}
	cache := newMapCache()
	c := CachedMoisture{Source: src, Cache: cache}

	for n := 0; n < 5; n++ {
		if _, err := c.Moisture(context.Background(), request(`[[[75.81,18.17],[75.82,18.17],[75.82,18.18],[75.81,18.17]]]`)); err != nil {
			t.Fatal(err)
		}
	}
	if got := src.calls.Load(); got != 1 {
		t.Fatalf("the service was asked %d times for one settled window", got)
	}
	for k, perm := range cache.permanent {
		if !perm {
			t.Errorf("a window that closed a year ago was stored as expiring: %s", k)
		}
	}
}

// The same ring with different whitespace is the same parcel.
func TestTheCacheKeyIgnoresFormatting(t *testing.T) {
	a, err := moistureKey(request(`[[[75.81,18.17],[75.82,18.17]]]`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := moistureKey(request(`[ [ [75.81, 18.17], [75.82, 18.17] ] ]`))
	if a != b {
		t.Fatal("reformatted coordinates produced a different cache key")
	}
	c, _ := moistureKey(request(`[[[75.81,18.17],[75.83,18.17]]]`))
	if a == c {
		t.Fatal("a different parcel produced the same cache key")
	}
}

// A development stack that cached a generated record would go on serving it
// after the service was pointed at Earth Engine.
func TestAGeneratedRecordIsNeverCached(t *testing.T) {
	src := &fakeMoisture{res: dryField("SYNTHETIC")}
	cache := newMapCache()
	c := CachedMoisture{Source: src, Cache: cache}

	_, _ = c.Moisture(context.Background(), request(`[[[75.81,18.17]]]`))
	_, _ = c.Moisture(context.Background(), request(`[[[75.81,18.17]]]`))

	if len(cache.rows) != 0 {
		t.Fatal("a generated record was written to the cache")
	}
	if src.calls.Load() != 2 {
		t.Fatalf("calls = %d, want every generated request to reach the service", src.calls.Load())
	}
}

// A row that cannot be decoded would fail on every read for as long as it is
// kept.
func TestAnUnreadableCacheRowIsReplaced(t *testing.T) {
	src := &fakeMoisture{res: dryField()}
	cache := newMapCache()
	req := request(`[[[75.81,18.17]]]`)
	key, _ := moistureKey(req)
	cache.Put(context.Background(), moistureProduct, key, req.StartDate, req.EndDate, []byte("{not json"), true)

	c := CachedMoisture{Source: src, Cache: cache}
	res, err := c.Moisture(context.Background(), req)
	if err != nil || res == nil {
		t.Fatalf("res = %v, err = %v", res, err)
	}
	if src.calls.Load() != 1 {
		t.Fatal("the corrupt row was not bypassed")
	}
	raw, _ := cache.Get(context.Background(), moistureProduct, key, req.StartDate, req.EndDate)
	if !json.Valid(raw) {
		t.Fatal("the corrupt row was not replaced")
	}
}

func absentMentions(p *Packet, word string) bool {
	for _, s := range p.Absent {
		if strings.Contains(strings.ToLower(s), word) {
			return true
		}
	}
	return false
}
