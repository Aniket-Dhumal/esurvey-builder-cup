// Package weather is the record of what the sky did over a field.
//
// # What was here before
//
// Fourteen days before the incident, fetched by the panel's weather
// specialist as a tool call. Two problems with that, and the second is worse
// than the first.
//
// Fourteen days cannot see a drought. It cannot see a dry spell that killed a
// crop in August for a claim filed in October, or a monsoon that arrived
// three weeks late so that the sowing date on the form is impossible, or the
// week above 38 °C during flowering that cost the yield before the hailstorm
// arrived. The tool always accepted a wider window; nothing ever asked for
// one.
//
// And a figure that enters an official record has to be reproducible during
// an appeal. A number a language model arrived at by calling a tool and
// summarising the answer is not reproducible in that sense. So the arithmetic
// happens here, deterministically, and the panel is given the derived facts
// rather than asked to derive them.
//
// # Why the archive rather than a forecast product
//
// A report is filed days or weeks after the loss. Forecast APIs keep a short
// history -- often a day or two -- so they cannot answer a question about a
// hailstorm three weeks ago at all. ERA5 through Open-Meteo's archive is free,
// needs no credential, and reaches back decades. Where a live product is
// available it is worth having as a cross-check, and it is not the
// evidentiary source.
package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/agronomy"
)

// Daily is one day as every weather source reports it.
type Daily struct {
	Date  string  `json:"date"`
	TMaxC float64 `json:"t_max_c"`
	TMinC float64 `json:"t_min_c"`

	RainMM      float64 `json:"rain_mm"`
	RainHours   float64 `json:"rain_hours"`
	WindMaxKmh  float64 `json:"wind_max_kmh"`
	GustMaxKmh  float64 `json:"gust_max_kmh"`
	WeatherCode int     `json:"weather_code"`
}

// Hail reports whether the day was coded as a thunderstorm with hail.
//
// WMO 96 and 99 are the only hail codes there are, and they are a weak proxy:
// they say hail was coded somewhere in an eleven-kilometre cell, not that it
// fell on this field. Named as a method rather than left as a magic number so
// that the weakness is visible at every call site.
func (d Daily) Hail() bool { return d.WeatherCode == 96 || d.WeatherCode == 99 }

// Thunder reports a thunderstorm of any kind.
func (d Daily) Thunder() bool { return d.WeatherCode >= 95 && d.WeatherCode <= 99 }

// Series is a run of days over one point.
type Series struct {
	Lat    float64 `json:"lat"`
	Lon    float64 `json:"lon"`
	Source string  `json:"source"`
	// ResolutionKm is how coarse the grid is, so nothing downstream reads a
	// district-scale figure as a field-scale one.
	ResolutionKm float64 `json:"resolution_km"`

	Days []Daily `json:"days"`

	// Missing is how many days in the requested range came back with nothing.
	Missing int `json:"missing_days"`
}

// Source labels.
const (
	SourceERA5    = "era5_archive"
	SourceGoogle  = "google_weather"
	dayLayout     = "2006-01-02"
	era5GridKm    = 11
	googleGridKm  = 1
	archiveMaxDay = 5 // ERA5 trails real time by about this many days
)

// Client fetches weather.
type Client struct {
	HTTP       *http.Client
	ArchiveURL string

	// Cache is optional. Weather is free to fetch and slow to fetch; a season
	// of daily records for one grid cell is requested by every farmer in that
	// cell, and there are hundreds of them after a district-wide event.
	Cache Cache
}

// Cache is the part of the shared remote-sensing cache this package uses.
type Cache interface {
	Get(ctx context.Context, product, key, start, end string) ([]byte, bool)
	Put(ctx context.Context, product, key, start, end string, raw []byte, permanent bool)
	Forget(ctx context.Context, product, key, start, end string)
}

func New(cache Cache) *Client {
	return &Client{
		// Generous: a season of daily records is a large response and this
		// runs behind an assessment that already takes seconds.
		HTTP:       &http.Client{Timeout: 45 * time.Second},
		ArchiveURL: "https://archive-api.open-meteo.com/v1/archive",
		Cache:      cache,
	}
}

// gridDegrees is how coarsely a point is snapped before asking.
//
// A tenth of a degree is about eleven kilometres, which is the resolution the
// reanalysis actually has. Asking for two points inside one grid cell returns
// the same numbers, so snapping loses nothing and lets every farmer in a
// locality answer from one fetch.
const gridDegrees = 0.1

func snap(lat, lon float64) (float64, float64) {
	return math.Round(lat/gridDegrees) * gridDegrees,
		math.Round(lon/gridDegrees) * gridDegrees
}

// Range fetches daily weather between two dates, inclusive.
func (c *Client) Range(ctx context.Context, lat, lon float64, from, to string) (*Series, error) {
	start, err := time.Parse(dayLayout, from)
	if err != nil {
		return nil, fmt.Errorf("from %q: %w", from, err)
	}
	end, err := time.Parse(dayLayout, to)
	if err != nil {
		return nil, fmt.Errorf("to %q: %w", to, err)
	}
	if end.Before(start) {
		return nil, fmt.Errorf("%s is before %s", to, from)
	}

	// The archive trails real time. Asking beyond its reach returns a short
	// series rather than an error, so the range is trimmed here and the
	// shortfall reported as missing days rather than appearing as a calm
	// fortnight that never happened.
	latest := time.Now().AddDate(0, 0, -archiveMaxDay)
	requested := int(end.Sub(start).Hours()/24) + 1
	if end.After(latest) {
		end = latest
	}
	if end.Before(start) {
		return nil, fmt.Errorf(
			"the archive reaches only to %s and the window starts at %s",
			latest.Format(dayLayout), from)
	}

	slat, slon := snap(lat, lon)
	key := fmt.Sprintf("%.1f,%.1f", slat, slon)
	trimmedTo := end.Format(dayLayout)

	if c.Cache != nil {
		if raw, ok := c.Cache.Get(ctx, "weather_daily", key, from, trimmedTo); ok {
			var s Series
			if err := json.Unmarshal(raw, &s); err == nil {
				s.Missing = requested - len(s.Days)
				return &s, nil
			}
			c.Cache.Forget(ctx, "weather_daily", key, from, trimmedTo)
		}
	}

	s, err := c.fetch(ctx, slat, slon, from, trimmedTo)
	if err != nil {
		return nil, err
	}
	s.Missing = requested - len(s.Days)

	if c.Cache != nil {
		if raw, err := json.Marshal(s); err == nil {
			// A closed window in a reanalysis archive is as immutable as a
			// satellite pass, and settles on the same schedule.
			c.Cache.Put(ctx, "weather_daily", key, from, trimmedTo, raw,
				time.Since(end) > 21*24*time.Hour)
		}
	}
	return s, nil
}

// archiveResponse is Open-Meteo's shape. Parallel arrays, one per variable.
type archiveResponse struct {
	Error  bool   `json:"error"`
	Reason string `json:"reason"`
	Daily  struct {
		Time    []string  `json:"time"`
		TMax    []float64 `json:"temperature_2m_max"`
		TMin    []float64 `json:"temperature_2m_min"`
		Rain    []float64 `json:"precipitation_sum"`
		RainHrs []float64 `json:"precipitation_hours"`
		WindMax []float64 `json:"wind_speed_10m_max"`
		GustMax []float64 `json:"wind_gusts_10m_max"`
		WxCode  []int     `json:"weather_code"`
	} `json:"daily"`
}

func (c *Client) fetch(ctx context.Context, lat, lon float64, from, to string) (*Series, error) {
	q := url.Values{}
	q.Set("latitude", fmt.Sprintf("%.4f", lat))
	q.Set("longitude", fmt.Sprintf("%.4f", lon))
	q.Set("start_date", from)
	q.Set("end_date", to)
	q.Set("daily", strings.Join([]string{
		"temperature_2m_max", "temperature_2m_min",
		"precipitation_sum", "precipitation_hours",
		"wind_speed_10m_max", "wind_gusts_10m_max",
		"weather_code",
	}, ","))
	// The record has to read in the timezone the officer reads it in, or a
	// hailstorm at 16:40 lands on the previous day.
	q.Set("timezone", "Asia/Kolkata")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.ArchiveURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}

	out, err := c.get(ctx, req)
	if err != nil {
		return nil, err
	}

	s := &Series{Lat: lat, Lon: lon, Source: SourceERA5, ResolutionKm: era5GridKm}
	d := out.Daily
	for i, day := range d.Time {
		s.Days = append(s.Days, Daily{
			Date:        day,
			TMaxC:       at(d.TMax, i),
			TMinC:       at(d.TMin, i),
			RainMM:      at(d.Rain, i),
			RainHours:   at(d.RainHrs, i),
			WindMaxKmh:  at(d.WindMax, i),
			GustMaxKmh:  at(d.GustMax, i),
			WeatherCode: atInt(d.WxCode, i),
		})
	}
	sort.Slice(s.Days, func(i, j int) bool { return s.Days[i].Date < s.Days[j].Date })
	return s, nil
}

// at reads a parallel array safely.
//
// Open-Meteo returns null for a variable it has no value for, which decodes
// to a short array when the whole variable is absent. Indexing blind would
// panic on exactly the days that are missing.
func at(v []float64, i int) float64 {
	if i < len(v) {
		return v[i]
	}
	return 0
}

func atInt(v []int, i int) int {
	if i < len(v) {
		return v[i]
	}
	return 0
}

// Days converts to the agronomy package's shape, so thermal time is computed
// from the same numbers the officer sees.
func (s *Series) Days2Agronomy() []agronomy.Day {
	out := make([]agronomy.Day, 0, len(s.Days))
	for _, d := range s.Days {
		out = append(out, agronomy.Day{Date: d.Date, TMaxC: d.TMaxC, TMinC: d.TMinC})
	}
	return out
}

// Retrying the weather archive.
//
// # Why this is here
//
// The archive answers 502 intermittently, with an HTML error page rather than
// JSON. It degrades correctly -- the source is reported absent and the
// assessment proceeds without it -- and that is worse than it sounds. Weather
// is the record of whether the calamity happened at all: a hailstorm claim
// assessed with no weather behind it has lost the evidence that there was a
// hailstorm.
//
// So a failure that a second attempt would have fixed should not become a
// permanent hole in a farmer's assessment record. The packet is stored with
// the assessment and is what an appeal reads, so the gap outlives the outage.
//
// # Why only these failures, and only twice
//
// A 5xx and a dropped connection are the archive having a bad moment. A 4xx is
// the archive refusing this request -- a date beyond its reach, a coordinate in
// the sea -- and asking again gets the same answer more slowly.
//
// Two retries, hundreds of milliseconds apart. The whole evidence assembly runs
// under a budget and shares it with radar, the registry and the panel; a
// generous retry here is taken out of another source's time, and an absent
// weather record is a better outcome than an absent everything.
const (
	fetchAttempts = 3
	fetchBackoff  = 400 * time.Millisecond
)

// get performs the request, retrying a transient failure.
func (c *Client) get(ctx context.Context, req *http.Request) (*archiveResponse, error) {
	var lastErr error
	for attempt := 0; attempt < fetchAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * fetchBackoff):
			}
		}

		out, retry, err := c.once(ctx, req)
		if err == nil {
			return out, nil
		}
		lastErr = err
		if !retry {
			return nil, err
		}
	}
	return nil, fmt.Errorf("%w (after %d attempts)", lastErr, fetchAttempts)
}

// once performs one attempt, and says whether trying again could help.
func (c *Client) once(ctx context.Context, req *http.Request) (*archiveResponse, bool, error) {
	// Cloned per attempt: a request whose body has been read cannot be sent
	// again, and Do may have consumed fields on the first pass.
	resp, err := c.HTTP.Do(req.Clone(ctx))
	if err != nil {
		// A dropped connection or a timeout on our side. Retryable unless the
		// caller has given up, in which case retrying burns the budget it was
		// trying to save.
		return nil, ctx.Err() == nil, fmt.Errorf("reaching the weather archive: %w", err)
	}
	defer resp.Body.Close()

	// The status is read before the body, because a 502 arrives as an HTML
	// error page and the decoder's complaint about it -- "invalid character
	// '<' looking for beginning of value" -- was once shown to officers as the
	// reason weather was missing.
	if resp.StatusCode >= 500 {
		return nil, true, fmt.Errorf(
			"the weather archive returned http %d", resp.StatusCode)
	}

	var out archiveResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		// A 200 that will not parse is a proxy or a captive portal answering
		// in the archive's place. Worth one more try.
		return nil, resp.StatusCode == http.StatusOK, fmt.Errorf(
			"decoding the weather archive (http %d): %w", resp.StatusCode, err)
	}
	if out.Error {
		// The archive's own words. It refuses several things precisely -- a
		// date beyond its reach, a bad coordinate -- and paraphrasing that
		// into "weather unavailable" loses the only useful part. Never
		// retried: the answer will not change.
		return nil, false, fmt.Errorf("the weather archive refused: %s", out.Reason)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf(
			"the weather archive returned http %d", resp.StatusCode)
	}
	return &out, false, nil
}
