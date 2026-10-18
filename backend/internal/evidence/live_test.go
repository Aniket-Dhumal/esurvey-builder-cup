package evidence

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/agribid/esurvey-platform/backend/internal/agronomy"
	"github.com/agribid/esurvey-platform/backend/internal/baseline"
	"github.com/agribid/esurvey-platform/backend/internal/claims"
	"github.com/agribid/esurvey-platform/backend/internal/fieldwork"
	"github.com/agribid/esurvey-platform/backend/internal/satellite"
	"github.com/agribid/esurvey-platform/backend/internal/weather"
)

// The assembly against the services it will actually run against.
//
// Guarded, because a suite that fails when a third party is down is one people
// learn to ignore. It is worth having because the unit tests use fakes for
// every source, and the thing they cannot check is whether the real analytics
// engine and the real weather archive answer the shapes this code parses --
// which is exactly how an integration breaks: silently, with every field zero.
//
//	SATELLITE_URL=... SATELLITE_API_KEY=... \
//	ESURVEY_TEST_DATABASE_URL=... \
//	ESURVEY_TEST_LIVE_EVIDENCE=1 go test ./internal/evidence/ -run Live -v

// malegaonParcel is the reference parcel from mock_files, which is a real
// boundary in Solapur district.
const malegaonRing = `[[[75.813582,18.1727367],[75.8128843,18.1727976],` +
	`[75.8128041,18.1717538],[75.8128121,18.1706871],[75.8126999,18.1695823],` +
	`[75.812645,18.1685947],[75.8127158,18.1680983],[75.8135,18.168],` +
	`[75.8142,18.1690],[75.8143,18.1705],[75.8141,18.1718],[75.813582,18.1727367]]]`

func TestLiveAssemblyAgainstTheRealServices(t *testing.T) {
	if os.Getenv("ESURVEY_TEST_LIVE_EVIDENCE") == "" {
		t.Skip("set ESURVEY_TEST_LIVE_EVIDENCE=1 to run against the live services")
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	}))

	a := &Assembler{Log: log, Budget: 4 * time.Minute}

	// Weather needs nothing but the network.
	a.Weather = weather.New(nil)

	// The analytics engine, where it is configured.
	if url := os.Getenv("SATELLITE_URL"); url != "" {
		eng := satellite.New(url, os.Getenv("SATELLITE_API_KEY"))
		a.Baselines = baseline.NewStore(nil, eng, log)
	} else {
		t.Log("SATELLITE_URL unset: baselines will report absent")
	}

	// Radar is its own service on the same contract.
	if url := os.Getenv("SAR_SERVICE_URL"); url != "" {
		a.Radar = satellite.New(url, "")
	} else {
		t.Log("SAR_SERVICE_URL unset: radar will report absent")
	}

	// The database, where it is configured.
	if url := os.Getenv("ESURVEY_TEST_DATABASE_URL"); url != "" {
		cfg, err := pgxpool.ParseConfig(url)
		if err == nil {
			cfg.MaxConns = 4
			if pool, err := pgxpool.NewWithConfig(context.Background(), cfg); err == nil {
				t.Cleanup(pool.Close)
				a.Claims = claims.NewStore(pool)
				a.Fieldwork = fieldwork.NewStore(pool)
			}
		}
	}

	// A window well inside every archive's reach.
	now := time.Now()
	incident := now.AddDate(0, 0, -30).Format("2006-01-02")
	sowing := now.AddDate(0, 0, -120).Format("2006-01-02")

	in := Input{
		FarmerID:     "live-check",
		FarmID:       "453",
		SurveyNumber: "453",
		VillageCode:  "562100",
		CropName:     "Soybean",
		Season:       "kharif",
		CropYear:     "2026-27",
		CalamityType: "unseasonal_rain",
		SowingDate:   sowing,
		IncidentDate: incident,
		Geometry: satellite.Geometry{
			Type: "Polygon", Coordinates: json.RawMessage(malegaonRing),
		},
		Lat: 18.170, Lon: 75.813, HasPoint: true,
		PeakNDVI: 0.62, RRVL: 35,
		DeclaredPercent: 50,
		FieldAreaHa:     1.6,
	}

	// A plausible season curve, so the sowing-date check has something to read.
	series := []agronomy.Observation{}
	for i, v := range []float64{0.16, 0.19, 0.34, 0.52, 0.62, 0.58, 0.41} {
		series = append(series, agronomy.Observation{
			Date: now.AddDate(0, 0, -120+i*15).Format("2006-01-02"), NDVI: v,
		})
	}

	started := time.Now()
	p := a.Assemble(context.Background(), in, series)
	elapsed := time.Since(started)

	if p == nil {
		t.Fatal("no packet")
	}
	t.Logf("assembled in %s (reported %s)", elapsed.Round(time.Millisecond), p.Elapsed)

	for _, s := range p.Absent {
		t.Logf("absent -- %s", s)
	}

	// Weather is the one source that needs no credential and no engine, so it
	// is the one this test insists on. If it is missing, either the archive
	// changed shape or the network is down, and both are worth failing for.
	if p.Weather == nil {
		t.Fatalf("no weather record: %v", p.Absent)
	}
	if p.Weather.Season.Days < 60 {
		t.Errorf("the season window covered %d days of about 90",
			p.Weather.Season.Days)
	}
	if p.Weather.Season.HottestC < 5 || p.Weather.Season.HottestC > 55 {
		t.Errorf("hottest day was %v °C; the variable names may have changed",
			p.Weather.Season.HottestC)
	}
	t.Logf("season: %d days, %.0f mm, longest dry spell %d days, hottest %.1f C",
		p.Weather.Season.Days, p.Weather.Season.RainMM,
		p.Weather.Season.LongestDrySpell, p.Weather.Season.HottestC)

	// The crop model turns those temperatures into a stage. This is the whole
	// of class C running on real numbers.
	if p.Thermal == nil {
		t.Fatalf("no growth stage was derived: %v", p.Absent)
	}
	if p.Thermal.AccumulatedGDD <= 0 {
		t.Errorf("accumulated GDD = %v over ninety days", p.Thermal.AccumulatedGDD)
	}
	t.Logf("stage: %s at %.0f GDD (%.0f%% of cycle), confidence %s",
		p.Thermal.Stage, p.Thermal.AccumulatedGDD,
		p.Thermal.PercentOfCycle, p.Thermal.Confidence)

	if p.Susceptibility != nil {
		t.Logf("susceptibility: %s at %s -- %s",
			p.Susceptibility.Level, p.Susceptibility.Stage, p.Susceptibility.Reason)
	}
	if p.SowingCheck != nil {
		t.Logf("sowing: declared %s, emergence %s, %s",
			p.SowingCheck.DeclaredDate, p.SowingCheck.ObservedDate,
			p.SowingCheck.Note)
	}
	if p.Denominators != nil && p.Denominators.History != nil {
		t.Logf("prior seasons: %d read, verdict %s",
			len(p.Denominators.History.Seasons), p.Denominators.History.Verdict)
	}
	if p.Denominators != nil && p.Denominators.Peer != nil {
		t.Logf("neighbourhood: %s -- %s",
			p.Denominators.Peer.Verdict, p.Denominators.Peer.Reason)
	}
	if p.Radar != nil {
		for _, f := range p.Radar.Findings {
			t.Logf("radar: %s (%s) -- %s", f.Code, f.Confidence, f.Detail)
		}
	}

	t.Logf("grounded: %v", p.Grounded())
	t.Logf("summary: %s", p.Summary())

	// The packet has to survive the wire to the panel.
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("the packet does not marshal: %v", err)
	}
	var round map[string]any
	if err := json.Unmarshal(raw, &round); err != nil {
		t.Fatalf("the packet does not round-trip: %v", err)
	}
	fmt.Fprintf(os.Stderr, "packet is %d bytes over the wire\n", len(raw))
	if len(raw) > 400_000 {
		t.Errorf("the packet is %d bytes; that is a large prompt", len(raw))
	}
}
