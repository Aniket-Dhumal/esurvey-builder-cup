package agristack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/singleflight"

	"github.com/agribid/esurvey-platform/backend/internal/platform/db"
)

// Registry is the farmer registry as the platform uses it: records it has been
// given, kept in Postgres, refreshed from an upstream registry when they are
// old, and served stale when that upstream is not answering.
//
// # Why not call the registry directly
//
// AgriStack answers for one farmer at a time, through an asynchronous
// seek/on-seek exchange, from a government deployment this platform does not
// run. At a lakh of claims a day, calling it on every sign-in, every lookup and
// every claim is a lakh of calls on its worst day, each of which can hang --
// and a farmer standing in their field is refused because a server in Delhi
// is slow. Nor can it be listed: there is no "every farmer in this village".
//
// So a record, once fetched, is the platform's until it is old
// (FreshFor), and an old one is refreshed on the next request for it. A
// refresh that fails -- timeout, error, circuit open -- serves the record the
// platform already had and says so in the log. Only a farmer the platform has
// never seen, while the registry is down, is refused, and refused as
// "unavailable" rather than "not found": those are different things to tell
// someone.
//
// # What a refresh is bounded by
//
//   - CallTimeout per attempt, and at most three attempts with backoff.
//   - A circuit breaker: after consecutive failures, calls stop for a
//     cooldown instead of every request waiting out the timeout.
//   - Singleflight: fifty requests for one farmer during an outage are one
//     call, not fifty.
//   - A short negative cache: an id the registry says does not exist is not
//     asked about again for a couple of minutes, so a mistyped id cannot be
//     used to hammer it.
type Registry struct {
	pool *db.Pool
	up   Upstream
	log  *slog.Logger

	// FreshFor is how old a record may be before it is refreshed.
	FreshFor time.Duration
	// CallTimeout bounds one attempt against the upstream.
	CallTimeout time.Duration

	// Reads, where set, serves the listings from a replica. Lookups by id
	// stay on the primary: a record just refreshed must be the one served.
	Reads *db.Router

	// OnRecord is told about every record that changed, in the transaction's
	// wake. Used to keep parcel boundaries in PostGIS current. Optional.
	OnRecord func(ctx context.Context, rec *FarmerRecord)

	sf      singleflight.Group
	breaker registryBreaker
	neg     negativeCache

	servedStale atomic.Int64
	refreshed   atomic.Int64
	failures    atomic.Int64
}

// Upstream is a registry that can answer for one farmer.
type Upstream interface {
	// Fetch returns the farmer's record, or ErrNotFound when the registry
	// holds none. Any other error is the registry failing to answer.
	Fetch(ctx context.Context, farmerID string) (*FarmerRecord, error)
	// Name labels records from it ("agristack", "file").
	Name() string
}

// ErrNotFound means the registry holds no record for the farmer.
var ErrNotFound = errors.New("no registry record for this farmer")

// ErrUnavailable means the registry did not answer and the platform has no
// earlier record to serve.
var ErrUnavailable = errors.New("the farmer registry is not answering; try again shortly")

const (
	defaultFreshFor    = 24 * time.Hour
	defaultCallTimeout = 8 * time.Second
	fetchAttempts      = 3
	negativeTTL        = 2 * time.Minute
)

// NewRegistry builds a registry over pool, refreshing from up. A nil upstream
// serves only what is already stored.
func NewRegistry(pool *db.Pool, up Upstream, log *slog.Logger) *Registry {
	if log == nil {
		log = slog.Default()
	}
	return &Registry{
		pool: pool, up: up, log: log,
		FreshFor: defaultFreshFor, CallTimeout: defaultCallTimeout,
	}
}

// Upstream is the registry records are refreshed from.
func (r *Registry) Upstream() Upstream { return r.up }

// FarmerRecord returns the farmer's record: stored and fresh, refreshed, or
// stored and stale when the refresh failed.
func (r *Registry) FarmerRecord(ctx context.Context, farmerID string) (*FarmerRecord, error) {
	farmerID = strings.TrimSpace(farmerID)
	if farmerID == "" || len(farmerID) > 64 {
		return nil, ErrNotFound
	}

	stored, fetchedAt, err := r.stored(ctx, farmerID)
	if err != nil {
		return nil, err
	}
	if stored != nil && (r.up == nil || time.Since(fetchedAt) < r.FreshFor) {
		return stored, nil
	}
	if r.up == nil {
		return nil, ErrNotFound
	}
	if stored == nil && r.neg.has(farmerID) {
		return nil, ErrNotFound
	}

	// Detached from the caller: the first of fifty callers giving up must not
	// cancel the one call the other forty-nine are waiting on. Bounded by the
	// attempts and their timeouts instead.
	v, err, _ := r.sf.Do(farmerID, func() (any, error) {
		return r.refresh(context.WithoutCancel(ctx), farmerID)
	})
	if err == nil {
		return v.(*FarmerRecord), nil
	}

	switch {
	case errors.Is(err, ErrNotFound) && stored == nil:
		r.neg.add(farmerID)
		return nil, ErrNotFound
	case stored != nil:
		// Gone from the registry, or the registry not answering: either way
		// the record the platform already acted on is served. A farmer
		// removed upstream is a matter for an officer, not for a sign-in
		// that suddenly fails.
		r.servedStale.Add(1)
		r.log.Warn("serving a stored registry record; the refresh failed",
			"farmer_id", farmerID, "age", time.Since(fetchedAt).Round(time.Minute).String(),
			"error", err)
		return stored, nil
	default:
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
}

func (r *Registry) refresh(ctx context.Context, farmerID string) (*FarmerRecord, error) {
	if !r.breaker.allow(time.Now()) {
		return nil, errBreakerOpen
	}

	var lastErr error
	for attempt := 1; attempt <= fetchAttempts; attempt++ {
		if attempt > 1 {
			t := time.NewTimer(time.Duration(attempt-1) * 400 * time.Millisecond)
			<-t.C
		}
		actx, cancel := context.WithTimeout(ctx, r.CallTimeout)
		rec, err := r.up.Fetch(actx, farmerID)
		cancel()

		if errors.Is(err, ErrNotFound) {
			// An answer, not a failure.
			r.breaker.succeeded()
			return nil, ErrNotFound
		}
		if err == nil {
			r.breaker.succeeded()
			if rec == nil || rec.Farmer.FarmerID != farmerID {
				return nil, fmt.Errorf("the registry answered for a different farmer")
			}
			if err := r.Put(ctx, rec, r.up.Name()); err != nil {
				// Stored or not, the record is the registry's answer.
				r.log.Error("storing a registry record", "farmer_id", farmerID, "error", err)
			}
			r.refreshed.Add(1)
			return rec, nil
		}
		lastErr = err
	}
	r.failures.Add(1)
	r.breaker.failed(time.Now())
	return nil, lastErr
}

var errBreakerOpen = errors.New("the registry has failed repeatedly; not calling it for a moment")

// stored reads the platform's copy.
func (r *Registry) stored(ctx context.Context, farmerID string) (*FarmerRecord, time.Time, error) {
	var raw []byte
	var fetchedAt time.Time
	err := r.pool.QueryRow(ctx,
		`SELECT record, fetched_at FROM registry_farmers WHERE farmer_id = $1`, farmerID).
		Scan(&raw, &fetchedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, time.Time{}, nil
	}
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("reading a stored registry record: %w", err)
	}
	var rec FarmerRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, time.Time{}, fmt.Errorf("decoding a stored registry record: %w", err)
	}
	return &rec, fetchedAt, nil
}

// Put stores a record as the registry's answer for its farmer.
//
// Unchanged records only have their fetch time moved, so a daily refresh of
// a lakh of farmers is a lakh of small updates rather than a lakh of rewrites.
func (r *Registry) Put(ctx context.Context, rec *FarmerRecord, source string) error {
	if rec == nil || rec.Farmer.FarmerID == "" {
		return errors.New("a registry record with no farmer id")
	}
	// Never more than the last four digits, whatever the registry sent. The
	// table refuses more too; this keeps a bad upstream from failing the
	// write instead of just not storing the number.
	rec.Farmer.Aadhaar = last4(rec.Farmer.Aadhaar)

	raw, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encoding a registry record: %w", err)
	}
	sum := sha256.Sum256(raw)
	sha := hex.EncodeToString(sum[:])

	f := summarise(rec)
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var changed bool
	if err := tx.QueryRow(ctx, `
		INSERT INTO registry_farmers (
			farmer_id, farmer_name, state_lgd_code, district_lgd_code,
			sub_district_lgd_code, village_lgd_code, mobile_no, aadhaar_last4,
			parcels, with_boundary, crops, extent_ha,
			record, record_sha, source, fetched_at, changed_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15, now(), now())
		ON CONFLICT (farmer_id) DO UPDATE SET
			farmer_name = EXCLUDED.farmer_name,
			state_lgd_code = EXCLUDED.state_lgd_code,
			district_lgd_code = EXCLUDED.district_lgd_code,
			sub_district_lgd_code = EXCLUDED.sub_district_lgd_code,
			village_lgd_code = EXCLUDED.village_lgd_code,
			mobile_no = EXCLUDED.mobile_no,
			aadhaar_last4 = EXCLUDED.aadhaar_last4,
			parcels = EXCLUDED.parcels,
			with_boundary = EXCLUDED.with_boundary,
			crops = EXCLUDED.crops,
			extent_ha = EXCLUDED.extent_ha,
			record = EXCLUDED.record,
			record_sha = EXCLUDED.record_sha,
			source = EXCLUDED.source,
			fetched_at = now(),
			changed_at = CASE WHEN registry_farmers.record_sha = EXCLUDED.record_sha
			                  THEN registry_farmers.changed_at ELSE now() END
		RETURNING changed_at = now()`,
		f.FarmerID, f.FarmerName, rec.StateLGDCode, f.DistrictCode,
		rec.Farmer.SubDistrictLGDCode, f.VillageCode, f.MobileNo, f.AadhaarLast4,
		f.Parcels, f.WithBoundary, f.Crops, f.ExtentHa,
		raw, sha, source).Scan(&changed); err != nil {
		return fmt.Errorf("storing a registry record: %w", err)
	}

	if changed {
		if _, err := tx.Exec(ctx,
			`DELETE FROM registry_parcels WHERE farmer_id = $1`, f.FarmerID); err != nil {
			return err
		}
		rows := parcelRows(rec)
		if len(rows) > 0 {
			if _, err := tx.CopyFrom(ctx, pgx.Identifier{"registry_parcels"},
				[]string{"farmer_id", "farm_id", "survey_number", "farmer_name",
					"village_lgd_code", "owner_extent_ha", "has_boundary",
					"crop_name", "joint_owners"},
				pgx.CopyFromSlice(len(rows), func(i int) ([]any, error) {
					p := rows[i]
					return []any{p.FarmerID, p.FarmID, p.SurveyNumber, p.FarmerName,
						p.VillageCode, p.OwnerExtent, p.HasBoundary, p.CropName,
						p.JointOwners}, nil
				})); err != nil {
				return fmt.Errorf("storing registry parcels: %w", err)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if changed && r.OnRecord != nil {
		r.OnRecord(ctx, rec)
	}
	return nil
}

// Enumerable is an upstream that can list what it holds -- a directory of
// registry responses can; AgriStack cannot.
type Enumerable interface {
	FarmerIDs() []string
	FarmerRecord(ctx context.Context, farmerID string) (*FarmerRecord, error)
}

// ImportAll stores every record an enumerable source holds.
func (r *Registry) ImportAll(ctx context.Context, src Enumerable, source string) (int, error) {
	n := 0
	for _, id := range src.FarmerIDs() {
		rec, err := src.FarmerRecord(ctx, id)
		if err != nil {
			return n, fmt.Errorf("reading farmer %s: %w", id, err)
		}
		if err := r.Put(ctx, rec, source); err != nil {
			return n, fmt.Errorf("storing farmer %s: %w", id, err)
		}
		n++
	}
	return n, nil
}

// --- listings -----------------------------------------------------------------

// Reach is whose records a listing may return.
//
// A farmer reaches their own; an officer the villages at or below their
// postings. Neither set means nothing: an empty reach is not "everywhere".
type Reach struct {
	FarmerID        string
	JurisdictionIDs []string
}

func (r Reach) empty() bool { return r.FarmerID == "" && len(r.JurisdictionIDs) == 0 }

// Boundary and crop filter values, as the console sends them.
const (
	BoundaryWith     = "with"
	BoundaryWithout  = "without"
	BoundaryMissing  = "missing"
	BoundaryComplete = "complete"
	CropUndeclared   = "undeclared"
)

// FarmerQuery is one page of farmers.
type FarmerQuery struct {
	Reach    Reach
	Search   string
	Boundary string
	Offset   int
	Limit    int
}

// reachSQL restricts alias's village_lgd_code / farmer_id to a reach, with the
// farmer id at $1 and the jurisdiction ids at $2.
func reachSQL(alias string) string {
	return `(CASE WHEN $1 <> '' THEN ` + alias + `.farmer_id = $1
	         ELSE ` + alias + `.village_lgd_code IN (
	              SELECT j.code FROM jurisdictions j
	               WHERE j.level = 'village'
	                 AND (j.id = ANY($2::uuid[]) OR j.ancestor_ids && $2::uuid[])) END)`
}

// likePattern escapes a search for ILIKE.
func likePattern(q string) string {
	q = strings.TrimSpace(q)
	if q == "" {
		return ""
	}
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(q) + "%"
}

// Farmers returns one page of farmers in reach, by name, and the total.
func (r *Registry) Farmers(ctx context.Context, q FarmerQuery) ([]FarmerSummary, int, error) {
	out := []FarmerSummary{}
	if q.Reach.empty() {
		return out, 0, nil
	}
	where := reachSQL("f") + `
		AND ($3 = '' OR f.farmer_name ILIKE $3 OR f.farmer_id ILIKE $3
		     OR f.village_lgd_code ILIKE $3 OR f.mobile_no ILIKE $3
		     OR f.aadhaar_last4 ILIKE $3)
		AND ($4 <> 'missing' OR f.parcels = 0 OR f.with_boundary < f.parcels)
		AND ($4 <> 'complete' OR (f.parcels > 0 AND f.with_boundary >= f.parcels))`
	args := []any{q.Reach.FarmerID, uuidsOrEmpty(q.Reach.JurisdictionIDs),
		likePattern(q.Search), q.Boundary}

	var total int
	if err := r.listPool().QueryRow(ctx,
		`SELECT count(*) FROM registry_farmers f WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting farmers: %w", err)
	}
	rows, err := r.listPool().Query(ctx, `
		SELECT f.farmer_id, f.farmer_name, f.village_lgd_code, f.district_lgd_code,
		       f.mobile_no, f.aadhaar_last4, f.parcels, f.extent_ha,
		       f.with_boundary, f.crops
		  FROM registry_farmers f WHERE `+where+`
		 ORDER BY f.farmer_name, f.farmer_id
		 LIMIT $5 OFFSET $6`, append(args, q.Limit, q.Offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("listing farmers: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var s FarmerSummary
		if err := rows.Scan(&s.FarmerID, &s.FarmerName, &s.VillageCode, &s.DistrictCode,
			&s.MobileNo, &s.AadhaarLast4, &s.Parcels, &s.ExtentHa,
			&s.WithBoundary, &s.Crops); err != nil {
			return nil, 0, err
		}
		out = append(out, s)
	}
	return out, total, rows.Err()
}

// ParcelQuery is one page of parcels.
type ParcelQuery struct {
	Reach    Reach
	Search   string
	Boundary string
	Crop     string
	Offset   int
	Limit    int
}

// Parcels returns one page of parcels in reach, by village and survey number.
func (r *Registry) Parcels(ctx context.Context, q ParcelQuery) ([]ParcelRow, int, error) {
	out := []ParcelRow{}
	if q.Reach.empty() {
		return out, 0, nil
	}
	where := reachSQL("p") + `
		AND ($3 = '' OR p.survey_number ILIKE $3 OR p.farm_id ILIKE $3
		     OR p.farmer_name ILIKE $3 OR p.village_lgd_code ILIKE $3
		     OR p.crop_name ILIKE $3)
		AND ($4 <> 'with' OR p.has_boundary)
		AND ($4 <> 'without' OR NOT p.has_boundary)
		AND ($5 = '' OR ($5 = 'undeclared' AND p.crop_name = '')
		     OR ($5 <> 'undeclared' AND lower(p.crop_name) = lower($5)))`
	args := []any{q.Reach.FarmerID, uuidsOrEmpty(q.Reach.JurisdictionIDs),
		likePattern(q.Search), q.Boundary, strings.TrimSpace(q.Crop)}

	var total int
	if err := r.listPool().QueryRow(ctx,
		`SELECT count(*) FROM registry_parcels p WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting parcels: %w", err)
	}
	rows, err := r.listPool().Query(ctx, `
		SELECT p.farm_id, p.survey_number, p.farmer_id, p.farmer_name,
		       p.village_lgd_code, p.owner_extent_ha, p.has_boundary,
		       p.crop_name, p.joint_owners
		  FROM registry_parcels p WHERE `+where+`
		 ORDER BY p.village_lgd_code, p.survey_number, p.farm_id
		 LIMIT $6 OFFSET $7`, append(args, q.Limit, q.Offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("listing parcels: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p ParcelRow
		if err := rows.Scan(&p.FarmID, &p.SurveyNumber, &p.FarmerID, &p.FarmerName,
			&p.VillageCode, &p.OwnerExtent, &p.HasBoundary, &p.CropName,
			&p.JointOwners); err != nil {
			return nil, 0, err
		}
		out = append(out, p)
	}
	return out, total, rows.Err()
}

// Totals is the registry inside a reach, added up.
type Totals struct {
	Farmers      int     `json:"farmers"`
	Parcels      int     `json:"parcels"`
	WithBoundary int     `json:"parcels_with_boundary"`
	Crops        int     `json:"crops"`
	ExtentHa     float64 `json:"extent_hectares"`
}

// Totals adds up the farmers in reach.
func (r *Registry) Totals(ctx context.Context, reach Reach) (Totals, error) {
	var t Totals
	if reach.empty() {
		return t, nil
	}
	err := r.listPool().QueryRow(ctx, `
		SELECT count(*), COALESCE(sum(f.parcels), 0), COALESCE(sum(f.with_boundary), 0),
		       COALESCE(sum(f.crops), 0), COALESCE(sum(f.extent_ha), 0)
		  FROM registry_farmers f WHERE `+reachSQL("f"),
		reach.FarmerID, uuidsOrEmpty(reach.JurisdictionIDs)).
		Scan(&t.Farmers, &t.Parcels, &t.WithBoundary, &t.Crops, &t.ExtentHa)
	if err != nil {
		return t, fmt.Errorf("totalling the registry: %w", err)
	}
	return t, nil
}

func (r *Registry) listPool() *db.Pool {
	if p := r.Reads.Read(); p != nil {
		return p
	}
	return r.pool
}

// RegistryStats is the registry's state, for readiness and the health page.
type RegistryStats struct {
	Upstream string `json:"upstream"`
	// Farmers is how many records are stored, estimated past a hundred
	// thousand so reading it never counts a large table.
	Farmers     int64 `json:"farmers"`
	Refreshed   int64 `json:"refreshed"`
	ServedStale int64 `json:"served_stale"`
	Failures    int64 `json:"failures"`
	CircuitOpen bool  `json:"circuit_open"`
}

// Stats reads the registry's state.
func (r *Registry) Stats(ctx context.Context) RegistryStats {
	s := RegistryStats{
		Refreshed: r.refreshed.Load(), ServedStale: r.servedStale.Load(),
		Failures: r.failures.Load(), CircuitOpen: !r.breaker.closed(),
	}
	if r.up != nil {
		s.Upstream = r.up.Name()
	}
	var est float64
	if err := r.pool.QueryRow(ctx,
		`SELECT reltuples FROM pg_class WHERE relname = 'registry_farmers'`).Scan(&est); err == nil && est > 100_000 {
		s.Farmers = int64(est)
		return s
	}
	_ = r.pool.QueryRow(ctx, `SELECT count(*) FROM registry_farmers`).Scan(&s.Farmers)
	return s
}

// --- helpers --------------------------------------------------------------------

func uuidsOrEmpty(ids []string) []string {
	if ids == nil {
		return []string{}
	}
	return ids
}

func last4(aadhaar string) string {
	a := strings.TrimSpace(aadhaar)
	if len(a) <= 4 {
		return a
	}
	return a[len(a)-4:]
}

// summarise is the listing row for a record.
func summarise(rec *FarmerRecord) FarmerSummary {
	sum := FarmerSummary{
		FarmerID:     rec.Farmer.FarmerID,
		FarmerName:   rec.Farmer.FarmerName,
		VillageCode:  rec.Farmer.VillageLGDCode,
		DistrictCode: rec.Farmer.DistrictLGDCode,
		MobileNo:     rec.Farmer.MobileNo,
		AadhaarLast4: rec.Farmer.Aadhaar,
		Parcels:      len(rec.Parcels),
		Crops:        len(rec.Crops),
	}
	for i := range rec.Parcels {
		if ha, err := rec.Parcels[i].ExtentHectares(); err == nil {
			sum.ExtentHa += ha
		}
		if rec.Parcels[i].PlotGeometry != nil {
			sum.WithBoundary++
		}
	}
	return sum
}

// parcelRows flattens a record's parcels, one per farm id.
func parcelRows(rec *FarmerRecord) []ParcelRow {
	seen := map[string]bool{}
	out := []ParcelRow{}
	for i := range rec.Parcels {
		p := &rec.Parcels[i]
		if p.FarmID == "" || seen[p.FarmID] {
			continue
		}
		seen[p.FarmID] = true
		row := ParcelRow{
			FarmID:       p.FarmID,
			SurveyNumber: p.SurveyNumber,
			FarmerID:     rec.Farmer.FarmerID,
			FarmerName:   rec.Farmer.FarmerName,
			VillageCode:  p.VillageLGDCode,
			HasBoundary:  p.PlotGeometry != nil,
			JointOwners:  len(p.JointOwners),
		}
		if ha, err := p.ExtentHectares(); err == nil {
			row.OwnerExtent = ha
		}
		for _, c := range rec.Crops {
			if c.FarmID == p.FarmID {
				row.CropName = c.CropName
				break
			}
		}
		out = append(out, row)
	}
	return out
}

// registryBreaker opens after consecutive failures and half-opens after a
// cooldown, like the satellite client's.
type registryBreaker struct {
	mu       sync.Mutex
	failures int
	openedAt time.Time
}

const (
	registryBreakerThreshold = 5
	registryBreakerCooldown  = 30 * time.Second
)

func (b *registryBreaker) allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failures < registryBreakerThreshold {
		return true
	}
	if now.Sub(b.openedAt) >= registryBreakerCooldown {
		b.openedAt = now
		return true
	}
	return false
}

func (b *registryBreaker) succeeded() {
	b.mu.Lock()
	b.failures = 0
	b.mu.Unlock()
}

func (b *registryBreaker) failed(now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures++
	if b.failures == registryBreakerThreshold {
		b.openedAt = now
	}
}

func (b *registryBreaker) closed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.failures < registryBreakerThreshold
}

// negativeCache remembers ids the registry said it does not hold.
type negativeCache struct {
	mu  sync.Mutex
	ids map[string]time.Time
}

const negativeCap = 10_000

func (c *negativeCache) has(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	until, ok := c.ids[id]
	if ok && time.Now().After(until) {
		delete(c.ids, id)
		return false
	}
	return ok
}

func (c *negativeCache) add(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ids == nil {
		c.ids = map[string]time.Time{}
	}
	if len(c.ids) >= negativeCap {
		// Bounded, crudely: an attacker filling it only costs the registry
		// the calls the cache would have saved.
		now := time.Now()
		for k, v := range c.ids {
			if now.After(v) {
				delete(c.ids, k)
			}
		}
		if len(c.ids) >= negativeCap {
			c.ids = map[string]time.Time{}
		}
	}
	c.ids[id] = time.Now().Add(negativeTTL)
}
