// Package maharain reads rainfall from Maharashtra's own rainfall portal.
//
// The state publishes, per revenue circle and per taluka, the rainfall actually
// recorded against the long-period normal for that unit. That pairing is the
// whole point: a drought determination in India is a departure from normal, and
// a platform holding only absolute millimetres from a reanalysis cannot express
// the finding at all.
//
// For Barshi taluka, kharif 2026, the two sources disagree by a factor of two:
//
//	ERA5 reanalysis   505 mm, no normal available
//	This source       286 mm against a normal of 516.6 mm -- 55.4%
//
// and in August alone, 19.4 mm against 124.5 mm, which is 15.6%. The district
// declared drought over that period. Only one of these two sources can support
// the declaration, and it is not the reanalysis.
//
// # What this package does not do
//
// It does not replace the reanalysis. The portal publishes daily figures only
// for completed years, so a dry spell inside the current season cannot be
// computed here. Amount and departure come from the state; distribution across
// days continues to come from ERA5. Every figure carries which.
//
// # It is a scraper, and says so
//
// There is no API. The portal renders HTML fragments from POST endpoints meant
// for its own page, and the markup is not well formed -- a whole table body
// arrives inside a single <tr>. Everything here is written to fail loudly on a
// shape it does not recognise rather than to guess, because a misparsed column
// would put one circle's rainfall against another's normal and nothing
// downstream could detect it.
package maharain

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// BaseURL is the portal's report root.
//
// The host is www. rather than bare: the certificate is issued to
// www.maharain.maharashtra.gov.in and the bare name does not match it.
const BaseURL = "https://www.maharain.maharashtra.gov.in/test/maharain"

// Source names this source in stored rows and in a report.
const Source = "maharain_mh_gov_in"

// MaharashtraStateCode is the portal's own code for the state. Hardcoded in the
// portal's own JavaScript too; it publishes one state.
const MaharashtraStateCode = "11"

// issuerPEM is the intermediate CA the portal's server does not send.
//
// The certificate is a real GlobalSign one for the right name. The server is
// simply misconfigured: it presents the leaf alone, so the chain to the trusted
// root cannot be built and every client rejects it. Browsers paper over this by
// caching intermediates from other sites; Go does not, and should not.
//
// The fix is to supply the missing link, not to stop checking. Ingesting a
// figure that will be quoted in a statutory assessment over a connection nobody
// authenticated would be indefensible -- and it is the connection most worth
// authenticating, because anyone able to sit in the middle of it could change a
// rainfall deficit.
//
// Fetched from the leaf's own Authority Information Access extension:
// http://secure.globalsign.com/cacert/gsgccr6alphasslca2025.crt
//
// It expires, and when it does this package fails closed with a certificate
// error rather than falling back to no verification. Replacing it is a
// deliberate act by a person who has checked what they are trusting.
const issuerPEM = `-----BEGIN CERTIFICATE-----
MIIFjTCCA3WgAwIBAgIRAIN9TriekS/nLK07x2kt3CAwDQYJKoZIhvcNAQELBQAw
TDEgMB4GA1UECxMXR2xvYmFsU2lnbiBSb290IENBIC0gUjYxEzARBgNVBAoTCkds
b2JhbFNpZ24xEzARBgNVBAMTCkdsb2JhbFNpZ24wHhcNMjUwNTIxMDIzNjUyWhcN
MjcwNTIxMDAwMDAwWjBVMQswCQYDVQQGEwJCRTEZMBcGA1UEChMQR2xvYmFsU2ln
biBudi1zYTErMCkGA1UEAxMiR2xvYmFsU2lnbiBHQ0MgUjYgQWxwaGFTU0wgQ0Eg
MjAyNTCCASIwDQYJKoZIhvcNAQEBBQADggEPADCCAQoCggEBAJ/oiu0Bviq52UUE
ADbFWmgu3rC7KDSMoorLN1Wd03McG3Z1aP71DlPCE33838r72Dfuj5M9LXfiQLJp
Au6MwNExmKOzothw4x0zGf5oBYyrCMGm3fBpLPafwYQ3MchBOWMTbf83rKUPLH48
KCJ0MnU8GUl8oA/J81wIvbbKPuNrFf6hvJDccjzc4NyxLz3A89zjV2g5whCg5O0u
9YX4Zxk9JHuc/LvllOJO4waAYLjbWBJkz3rV3ts1SmSYnJqmyRTIjXwQgRvhEYqt
DbRskt0W7M6cPwCze3GTBN2UHNpHkMs3YmVxku68I0aOQn5+uz//fDROP3z1Z/7I
APteRtECAwEAAaOCAV8wggFbMA4GA1UdDwEB/wQEAwIBhjAdBgNVHSUEFjAUBggr
BgEFBQcDAQYIKwYBBQUHAwIwEgYDVR0TAQH/BAgwBgEB/wIBADAdBgNVHQ4EFgQU
xbSTj28r3B5Iv7cQMIXO0bK7SC0wHwYDVR0jBBgwFoAUrmwFo5MT4qLn4tcc1sfw
f8hnU6AwewYIKwYBBQUHAQEEbzBtMC4GCCsGAQUFBzABhiJodHRwOi8vb2NzcDIu
Z2xvYmFsc2lnbi5jb20vcm9vdHI2MDsGCCsGAQUFBzAChi9odHRwOi8vc2VjdXJl
Lmdsb2JhbHNpZ24uY29tL2NhY2VydC9yb290LXI2LmNydDA2BgNVHR8ELzAtMCug
KaAnhiVodHRwOi8vY3JsLmdsb2JhbHNpZ24uY29tL3Jvb3QtcjYuY3JsMCEGA1Ud
IAQaMBgwCAYGZ4EMAQIBMAwGCisGAQQBoDIKAQMwDQYJKoZIhvcNAQELBQADggIB
AB/uvBuZf4CiuSahwiXn4geF52roAH+6jxsEPTXTfb7bbeMDXsYgRRsOTNA70ruZ
Tnz5DfFMuBhNoFhIFb0qR1izdy6VkdKOqFPNF2dOFI1EcnY9l2ory9mrzHqVbrL4
vzUd17FLUVyjTVU7PAv4nxyhnO1GTeT83YlrdRF31NyR6bvZVTEERHmpbWSgeveJ
LRtaMzlGWiLZ8IwkH7o6GH3jp/KPtDW4Npu8w64HrRZdN2pqQhi7+YKwfHM7H+2U
dM1BGN0sjOWMVbMSB9MtCsleS2Mb7TRZEbOHxECJLLIluQypZr7Pol3+hAqrhyKI
k+6y+Da0NeDuWxW59Ku4NvClqW1UFX1SpfNGhzVfp/CH+vPM1tySomx2jE0EnYZu
GwVucXPBsp5nUWqUV9+143glVuS7GTg9hFPjNBInn17HbCoIIQIOzj5Vd9bK3A9U
GxXNpwenDHEalCsD/4eQYDHPhFE7sNe0D/OXu+FAM02VZkARx37Jp4bDdujvgL9P
vZPR3wThvDN1CTU8Bc3xea3yKFAraKcPZLkhReQUAm2VpR+HSJRPlUpYizlF9WkL
h3KcAVCBJWvnOkVwxyU5QJMcnwW95JlOtx+9100GL99jHE5rs3gXp7F4bg8H01QT
9jVOhBBmQ7nQoXuwI0tqal2QUqZz3eeu62CU7xBwtfYR
-----END CERTIFICATE-----
`

// MinInterval is the shortest gap this package will leave between two report
// requests.
//
// The reports are not files being served. Each one is generated on demand by
// summing a district's gauges for the whole season, and a district-wide circle
// report covers 91 units. This is somebody's public service running on a budget,
// and it is the only source for a figure farmers' claims now depend on: taking
// it down would be both rude and self-defeating.
//
// So requests are serialised and spaced. There is no concurrency here and no
// option to add it.
const MinInterval = 6 * time.Second

// MaxReportsPerRun bounds one invocation.
//
// A cap rather than a throttle alone, because the failure worth preventing is a
// loop: a bug in district enumeration that asks for the same report repeatedly
// would otherwise be indistinguishable from an attack. Reaching it is an error,
// not a quiet stop.
const MaxReportsPerRun = 40

// Client reads the portal.
//
// One client, used sequentially. It holds the time of its last report request
// and sleeps before the next one, so the pacing cannot be forgotten by a caller.
type Client struct {
	HTTP    *http.Client
	BaseURL string

	// UserAgent identifies this platform to the portal's operators. A scraper
	// that cannot be traced back to somebody who will answer the phone is a
	// scraper that gets blocked, and deserves to be.
	UserAgent string

	lastReport time.Time
	reports    int
}

// New builds a client that verifies the portal's certificate against the system
// roots plus the intermediate the portal omits.
func New() (*Client, error) {
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("reading the system certificate pool: %w", err)
	}
	if !roots.AppendCertsFromPEM([]byte(issuerPEM)) {
		return nil, errors.New(
			"the bundled GlobalSign intermediate did not parse, so the portal's " +
				"certificate cannot be verified")
	}

	return &Client{
		// The reports are generated per request over a whole district and are
		// slow. Nothing here is on a farmer's path: it runs in a loader.
		HTTP: &http.Client{
			Timeout: 120 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					RootCAs:    roots,
					MinVersion: tls.VersionTLS12,
				},
			},
		},
		BaseURL: BaseURL,
		UserAgent: "eSurvey-CropLoss/1.0 (Maharashtra crop-loss assessment " +
			"platform; rainfall loader; contact the platform operator)",
	}, nil
}

// pace blocks until enough time has passed since the last report request, and
// refuses once the per-run cap is reached.
func (c *Client) pace(ctx context.Context) error {
	c.reports++
	if c.reports > MaxReportsPerRun {
		return fmt.Errorf(
			"refusing to ask for more than %d reports in one run: something is "+
				"asking in a loop, and this is a public service",
			MaxReportsPerRun)
	}
	if c.lastReport.IsZero() {
		c.lastReport = time.Now()
		return nil
	}

	if wait := MinInterval - time.Since(c.lastReport); wait > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	c.lastReport = time.Now()
	return nil
}

// District is one district as the portal identifies it.
type District struct {
	DivisionCode string `json:"-"`
	Code         string `json:"district_code"`
	Name         string `json:"district_name"`
}

// Divisions the portal publishes. Six revenue divisions; 7 and 8 answer with an
// empty list, which is how the range was established rather than assumed.
var Divisions = []string{"1", "2", "3", "4", "5", "6"}

// Districts lists the districts of one division.
func (c *Client) Districts(ctx context.Context, division string) ([]District, error) {
	body, err := c.post(ctx, "Handlers/districtHandler.php", url.Values{
		"getDistrict":  {"1"},
		"stateCode":    {MaharashtraStateCode},
		"divisionCode": {division},
	})
	if err != nil {
		return nil, err
	}

	out, err := parseDistricts(body)
	if err != nil {
		return nil, fmt.Errorf("division %s: %w", division, err)
	}
	for i := range out {
		out[i].DivisionCode = division
	}
	return out, nil
}

// Level is the administrative level a report is published at.
type Level string

const (
	// LevelTaluka is the unit drought is declared at, and the finest unit a
	// parcel can currently be resolved to with certainty.
	LevelTaluka Level = "sub_district"
	// LevelCircle is finer and is stored for when a village-to-circle mapping
	// exists to make it usable.
	LevelCircle Level = "circle"
)

// endpoint is the report path for a level. Both return the same seventeen-column
// shape, which is why one parser serves both.
func endpoint(l Level) (string, error) {
	switch l {
	case LevelTaluka:
		return "rpt_tehsil_wise_report_month_wise_with_total.php", nil
	case LevelCircle:
		return "rpt_circle_wise_report_month_wise_total.php", nil
	default:
		return "", fmt.Errorf("no monthly report is published at level %q", l)
	}
}

// MonthlyReport fetches the month-by-month rainfall of one district at one
// level, with the season-to-date cumulative.
func (c *Client) MonthlyReport(
	ctx context.Context, level Level, d District,
) (*Report, error) {
	path, err := endpoint(level)
	if err != nil {
		return nil, err
	}
	if err := c.pace(ctx); err != nil {
		return nil, err
	}

	body, err := c.post(ctx, path, url.Values{
		"stateCode":    {MaharashtraStateCode},
		"divisionCode": {d.DivisionCode},
		"districtCode": {d.Code},
	})
	if err != nil {
		return nil, err
	}

	rep, err := parseMonthly(body)
	if err != nil {
		return nil, fmt.Errorf("%s district %s (%s): %w",
			level, d.Name, d.Code, err)
	}
	rep.Level = level
	rep.District = d
	return rep, nil
}

func (c *Client) post(ctx context.Context, path string, form url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/"+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// The endpoints are meant for the portal's own page. Asking as a browser
	// would is honest about what this is and avoids a refusal that would be
	// read as an empty district.
	req.Header.Set("Accept", "text/html, */*; q=0.01")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Referer", c.BaseURL+"/current_year_rain.php")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Capped. A misrouted request can return the whole portal page, and an
	// unbounded read of a page this loader is about to reject is pointless.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("%s: reading the response: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: the portal answered %s", path, resp.Status)
	}
	return body, nil
}
