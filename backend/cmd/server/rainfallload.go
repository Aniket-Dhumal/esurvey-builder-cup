package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/config"
	"github.com/agribid/esurvey-platform/backend/internal/maharain"
	"github.com/agribid/esurvey-platform/backend/internal/platform/db"
)

// rainfallLoad reads the state's published rainfall for one district.
//
//	esurvey-server rainfall-load Solapur              # one taluka report
//	esurvey-server rainfall-load Solapur --circles    # and the circle report
//	esurvey-server rainfall-load Solapur --force      # ignore today's figures
//	esurvey-server rainfall-load --all-districts      # the whole state, slowly
//
// # It asks for as little as it can
//
// A district is required. There is no default of "everything": the reports are
// generated on demand by a public service summing a district's gauges, and the
// earlier version of this command would have asked for seventy of them back to
// back on first run. One district at taluka level is one request.
//
// Three things keep it small:
//
//   - The taluka report alone, unless circles are asked for. Taluka is the unit
//     drought is declared at and the finest unit a parcel resolves to here, so
//     the circle report is 91 rows this platform cannot yet attach to anything.
//   - Nothing is fetched twice in a day. The portal publishes once each morning;
//     a second run the same afternoon would generate the report again to receive
//     numbers already stored.
//   - Requests are serialised and spaced, and the client refuses beyond a cap
//     per run.
//
// --all-districts exists because the data will eventually be wanted statewide,
// and it is deliberately slow: thirty-five districts at six seconds apart is
// about four minutes of wall clock, once a day, which is a rounding error on
// somebody's web server. It is never the default.
func rainfallLoad(args []string) int {
	log := newLogger(config.Development)

	var district string
	var wantCircles, force, all bool
	for _, a := range args {
		switch {
		case a == "--circles":
			wantCircles = true
		case a == "--force":
			force = true
		case a == "--all-districts":
			all = true
		case strings.HasPrefix(a, "-"):
			log.Error("unknown option", "option", a)
			return 1
		default:
			district = strings.TrimSpace(a)
		}
	}
	if district == "" && !all {
		log.Error("name a district, or pass --all-districts deliberately",
			"example", "rainfall-load Solapur")
		return 1
	}

	cfg, err := config.Load()
	if err != nil {
		log.Error("configuration", "error", err)
		return 1
	}

	// Enough for the whole state at the pace the client enforces, and no more.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	pool, err := db.OpenFor(ctx, cfg.Database, db.Direct)
	if err != nil {
		log.Error("database", "error", err)
		return 1
	}
	defer pool.Close()

	client, err := maharain.New()
	if err != nil {
		log.Error("building the rainfall client", "error", err)
		return 1
	}
	store := &maharain.Store{Pool: pool}

	// The geography handlers are small JSON lookups, not generated reports, and
	// the district list does not change. Still asked for once per run rather
	// than per district.
	districts, err := findDistricts(ctx, client, district)
	if err != nil {
		log.Error("listing districts", "error", err)
		return 1
	}
	if len(districts) == 0 {
		log.Error("no district of Maharashtra has that name", "asked_for", district)
		return 1
	}

	levels := []maharain.Level{maharain.LevelTaluka}
	if wantCircles {
		levels = append(levels, maharain.LevelCircle)
	}

	year := cropYearNow()
	var rows, fetched, skipped, failures int
	var unmatched []string

	for _, d := range districts {
		for _, level := range levels {
			if !force {
				fresh, err := store.FetchedToday(ctx, level, d.Code, year)
				if err != nil {
					log.Error("checking what is already stored",
						"district", d.Name, "error", err)
					failures++
					continue
				}
				if fresh {
					skipped++
					log.Info("already current, not asking again",
						"district", d.Name, "level", level)
					continue
				}
			}

			rep, err := client.MonthlyReport(ctx, level, d)
			if err != nil {
				// One district failing must not lose the rest, but a refusal
				// from the pacing cap means stop entirely rather than carry on
				// asking.
				log.Error("fetching the report",
					"district", d.Name, "level", level, "error", err)
				failures++
				if strings.Contains(err.Error(), "refusing to ask") {
					return 1
				}
				continue
			}
			fetched++

			saved, err := store.Save(ctx, rep)
			if err != nil {
				log.Error("storing the report",
					"district", d.Name, "level", level, "error", err)
				failures++
				continue
			}
			rows += saved.Rows

			log.Info("stored",
				"district", d.Name,
				"level", level,
				"units", len(rep.Units),
				"periods", len(rep.Periods),
				"rows", saved.Rows,
				"matched", saved.Matched,
				"unmatched", len(saved.Unmatched))

			for _, name := range saved.Unmatched {
				unmatched = append(unmatched,
					fmt.Sprintf("%s/%s/%s", d.Name, level, name))
			}
		}
	}

	// The one thing a person has to act on: a published unit whose rainfall is
	// stored and cannot be attached to an assessment because its name is spelt
	// differently here.
	if len(unmatched) > 0 {
		fmt.Fprintf(os.Stderr,
			"\n%d published units did not match a jurisdiction by name.\n"+
				"Their rainfall is stored and is attached to nothing.\n"+
				"Reconcile the names; do not loosen the match:\n",
			len(unmatched))
		for _, u := range unmatched {
			fmt.Fprintf(os.Stderr, "  %s\n", u)
		}
	}

	log.Info("done",
		"rows", rows, "reports_fetched", fetched,
		"already_current", skipped, "failed", failures)
	if failures > 0 {
		return 1
	}
	return 0
}

// findDistricts resolves a district by name, or lists them all.
func findDistricts(
	ctx context.Context, c *maharain.Client, only string,
) ([]maharain.District, error) {
	var out []maharain.District
	for _, div := range maharain.Divisions {
		ds, err := c.Districts(ctx, div)
		if err != nil {
			return nil, err
		}
		for _, d := range ds {
			if only != "" && !strings.EqualFold(d.Name, only) {
				continue
			}
			out = append(out, d)
			// A named district is in exactly one division; stop rather than
			// asking the remaining ones.
			if only != "" {
				return out, nil
			}
		}
	}
	return out, nil
}

// cropYearNow is the year the current monsoon season belongs to.
//
// The portal's season runs June to December, so a run in January is still
// reading the previous year's season and must not ask for a year the portal has
// no current-season figures for.
func cropYearNow() int {
	now := time.Now()
	if now.Month() < time.June {
		return now.Year() - 1
	}
	return now.Year()
}
