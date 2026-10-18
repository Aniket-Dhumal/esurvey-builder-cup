package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Google's Weather API, as a cross-check and never as the record.
//
// # What it is good for
//
// Finer resolution than an eleven-kilometre reanalysis grid, and fields the
// archive does not carry at all -- thunderstorm probability, dew point, cloud
// cover, gusts at a scale closer to a field than to a taluka. Where a farmer
// files within a day or two of the loss, that is a materially better picture
// of what happened over their land.
//
// # Why it is not the record
//
// Two reasons, and either alone would be enough.
//
// It is a nowcast and forecast product. Its hourly history reaches back hours,
// not weeks, and reports arrive days to weeks after the loss -- so for most
// claims it cannot answer the question at all. Trying anyway would produce a
// confident answer about the wrong days.
//
// And Maps Platform restricts storing results. An assessment must be
// reproducible during an appeal, which means the weather that justified it has
// to be kept alongside it. Until somebody has read those terms against this
// use, nothing from here is written into an assessment record: it is fetched,
// compared against the archive, and the comparison is what is kept.
//
// # Consequently
//
// This is off unless a key is configured, it never replaces a series, and its
// only output is a note saying whether the two sources agree.

// GoogleClient talks to the Maps Platform Weather API.
type GoogleClient struct {
	HTTP    *http.Client
	BaseURL string
	APIKey  string
}

// NewGoogle builds a client. An empty key leaves it disabled, which is the
// deployed state until somebody enables the API and reviews the terms.
func NewGoogle(apiKey string) *GoogleClient {
	return &GoogleClient{
		HTTP:    &http.Client{Timeout: 20 * time.Second},
		BaseURL: "https://weather.googleapis.com/v1",
		APIKey:  strings.TrimSpace(apiKey),
	}
}

// Enabled reports whether a key was configured.
func (g *GoogleClient) Enabled() bool { return g != nil && g.APIKey != "" }

// HistoryReachHours is how far back the hourly history is asked for.
//
// Twenty-four is the documented reach at the time of writing. It is a
// constant rather than a parameter because asking for more does not extend
// it, and a caller who believed it had would be reasoning about days that
// came back empty.
const HistoryReachHours = 24

// googleHistory is the subset of the response this uses.
type googleHistory struct {
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
	HistoryHours []struct {
		Interval struct {
			StartTime string `json:"startTime"`
		} `json:"interval"`
		Temperature struct {
			Degrees float64 `json:"degrees"`
			Unit    string  `json:"unit"`
		} `json:"temperature"`
		Precipitation struct {
			QPF struct {
				Quantity float64 `json:"quantity"`
				Unit     string  `json:"unit"`
			} `json:"qpf"`
		} `json:"precipitation"`
		Wind struct {
			Gust struct {
				Value float64 `json:"value"`
				Unit  string  `json:"unit"`
			} `json:"gust"`
		} `json:"wind"`
	} `json:"historyHours"`
}

// Recent is what the last day looked like over a point.
type Recent struct {
	Hours      int     `json:"hours"`
	RainMM     float64 `json:"rain_mm"`
	MaxGustKmh float64 `json:"max_gust_kmh"`
	MaxTempC   float64 `json:"max_temp_c"`
	MinTempC   float64 `json:"min_temp_c"`
	From       string  `json:"from,omitempty"`
	To         string  `json:"to,omitempty"`
}

// LastDay reads the recent hourly history over a point.
//
// Returns false with no error when no key is configured, because that is the
// ordinary deployed state and not a fault.
func (g *GoogleClient) LastDay(ctx context.Context, lat, lon float64) (*Recent, bool, error) {
	if !g.Enabled() {
		return nil, false, nil
	}

	q := url.Values{}
	q.Set("key", g.APIKey)
	q.Set("location.latitude", fmt.Sprintf("%.4f", lat))
	q.Set("location.longitude", fmt.Sprintf("%.4f", lon))
	q.Set("hours", fmt.Sprintf("%d", HistoryReachHours))
	q.Set("unitsSystem", "METRIC")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		g.BaseURL+"/history/hours:lookup?"+q.Encode(), nil)
	if err != nil {
		return nil, false, err
	}

	resp, err := g.HTTP.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("reaching the Google weather API: %w", err)
	}
	defer resp.Body.Close()

	var out googleHistory
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, false, fmt.Errorf(
			"decoding the Google weather API (http %d): %w", resp.StatusCode, err)
	}
	if out.Error != nil {
		// Its own words. "API not enabled", "billing not configured" and
		// "referer blocked" are all operator actions, and paraphrasing them
		// into "weather unavailable" sends somebody debugging the wrong thing.
		return nil, false, fmt.Errorf("the Google weather API refused: %s (%s)",
			out.Error.Message, out.Error.Status)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf(
			"the Google weather API returned http %d", resp.StatusCode)
	}
	if len(out.HistoryHours) == 0 {
		return nil, false, nil
	}

	r := &Recent{Hours: len(out.HistoryHours)}
	first := true
	for _, h := range out.HistoryHours {
		r.RainMM += h.Precipitation.QPF.Quantity
		if h.Wind.Gust.Value > r.MaxGustKmh {
			r.MaxGustKmh = h.Wind.Gust.Value
		}
		t := h.Temperature.Degrees
		if first {
			r.MaxTempC, r.MinTempC = t, t
			r.From = h.Interval.StartTime
			first = false
		}
		if t > r.MaxTempC {
			r.MaxTempC = t
		}
		if t < r.MinTempC {
			r.MinTempC = t
		}
		r.To = h.Interval.StartTime
	}
	r.RainMM = round1(r.RainMM)
	r.MaxGustKmh = round1(r.MaxGustKmh)
	return r, true, nil
}

// CrossCheck compares a live reading against the archive's last day.
//
// The output is a sentence, not a number. Two sources at different
// resolutions will differ, and the useful question is whether they differ
// enough to doubt the coarser one -- not what the difference was.
func CrossCheck(archive Window, live *Recent) string {
	if live == nil {
		return ""
	}

	switch {
	case live.RainMM >= 10 && archive.MaxDailyMM < 2:
		return fmt.Sprintf(
			"a live reading over this point records %.0f mm in the last %d hours "+
				"where the reanalysis grid shows almost none. The grid is eleven "+
				"kilometres across and can miss a convective cell entirely.",
			live.RainMM, live.Hours)
	case live.MaxGustKmh >= 60 && archive.MaxGustKmh < 40:
		return fmt.Sprintf(
			"a live reading records a %.0f km/h gust over this point that the "+
				"reanalysis grid does not show", live.MaxGustKmh)
	case live.RainMM < 1 && archive.MaxDailyMM >= 20:
		return fmt.Sprintf(
			"a live reading over this point shows no rain in the last %d hours "+
				"while the reanalysis grid records %.0f mm on its wettest day; the "+
				"grid figure may belong to a neighbouring cell",
			live.Hours, archive.MaxDailyMM)
	default:
		return fmt.Sprintf(
			"a live reading over this point agrees with the reanalysis grid to "+
				"within what two sources at different resolutions would differ by "+
				"(%.0f mm live over %d hours)", live.RainMM, live.Hours)
	}
}
