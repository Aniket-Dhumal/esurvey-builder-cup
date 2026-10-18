package agristack

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FileSource serves farmer records from registry responses on disk.
//
// # Why a directory rather than a live client
//
// AgriStack returns plot_geometry: null and an empty crop_sown_data for every
// farmer we can reach; see mock_files/README.md. Until that changes, the
// registry response is something an operator obtains out of band and places
// in a directory. That is also how the planned trials will run: a record is
// dropped in by hand and the platform picks it up.
//
// # What that requires of this loader
//
// A file added while the service is running must be served without a restart,
// because "restart the API to onboard a farmer" is not a procedure anyone will
// follow correctly during a trial. So the directory is re-read when it has
// changed, and only when it has changed.
//
// Equally, one malformed file must not take the others down. A trial where a
// bad paste silently unregisters every farmer already onboarded is worse than
// one where a single record is missing and the reason is on the /readyz page.
type FileSource struct {
	dir string

	// minInterval throttles the stat sweep. A record lookup happens on every
	// assessment, and re-stat'ing the directory each time would turn a cheap
	// map read into filesystem traffic for no benefit -- a file dropped in by
	// hand is not needed within the same second it lands.
	minInterval time.Duration

	mu        sync.RWMutex
	records   map[string]*FarmerRecord
	origin    map[string]string // farmer id -> file it came from
	problems  []FileProblem
	scanned   map[string]fileStamp
	lastCheck time.Time
	loadedAt  time.Time
}

type fileStamp struct {
	size    int64
	modTime time.Time
}

// FileProblem is one file that could not be read, kept so an operator can be
// told which paste was wrong instead of discovering an absence.
type FileProblem struct {
	File   string `json:"file"`
	Reason string `json:"reason"`
}

// Status is what the loader can report about itself.
type Status struct {
	Dir      string        `json:"dir"`
	Farmers  int           `json:"farmers"`
	Files    int           `json:"files"`
	LoadedAt time.Time     `json:"loaded_at"`
	Problems []FileProblem `json:"problems,omitempty"`
}

// NewFileSource reads dir and watches it for changes.
//
// An empty or absent directory is not an error. The service starts, says so,
// and begins serving the moment a record is placed there -- which is the whole
// point of loading from a directory rather than compiling records in.
func NewFileSource(dir string) (*FileSource, error) {
	s := &FileSource{
		dir:         dir,
		minInterval: time.Second,
		records:     map[string]*FarmerRecord{},
		origin:      map[string]string{},
		scanned:     map[string]fileStamp{},
	}
	if _, err := s.reload(); err != nil {
		return nil, err
	}
	return s, nil
}

// candidates lists the files worth trying to parse.
//
// Every .json in the directory, not a fixed naming pattern. An operator
// dropping a record in during a trial should not have to know that it must be
// called something.i1004_o1007.json for the platform to look at it.
func (s *FileSource) candidates() ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(s.dir, "*.json"))
	if err != nil {
		return nil, fmt.Errorf("scanning %s: %w", s.dir, err)
	}
	sort.Strings(paths)
	return paths, nil
}

// changed reports whether the directory differs from the last read.
func (s *FileSource) changed(paths []string) (map[string]fileStamp, bool) {
	now := map[string]fileStamp{}
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			// Unreadable now; reload will record why.
			now[p] = fileStamp{}
			continue
		}
		now[p] = fileStamp{size: info.Size(), modTime: info.ModTime()}
	}

	s.mu.RLock()
	prev := s.scanned
	s.mu.RUnlock()

	if len(prev) != len(now) {
		return now, true
	}
	for p, st := range now {
		was, ok := prev[p]
		if !ok || was != st {
			return now, true
		}
	}
	return now, false
}

// reload re-reads the directory. It returns an error only for a failure that
// makes the directory unusable; a single bad file is recorded as a problem.
func (s *FileSource) reload() (Status, error) {
	paths, err := s.candidates()
	if err != nil {
		return Status{}, err
	}
	stamps, _ := s.changed(paths)

	loaded := map[string]*FarmerRecord{}
	origin := map[string]string{}
	var problems []FileProblem

	for _, p := range paths {
		name := filepath.Base(p)
		raw, err := os.ReadFile(p)
		if err != nil {
			problems = append(problems, FileProblem{name, "could not be read: " + err.Error()})
			continue
		}
		recs, err := parseRecords(raw)
		if err != nil {
			problems = append(problems, FileProblem{name, err.Error()})
			continue
		}
		for _, rec := range recs {
			id := rec.Farmer.FarmerID
			if id == "" {
				problems = append(problems, FileProblem{name, "a record has no farmer_id"})
				continue
			}
			if prev, dup := origin[id]; dup {
				problems = append(problems, FileProblem{
					name, fmt.Sprintf("farmer %s is also in %s; the later file wins", id, prev),
				})
			}
			loaded[id] = rec
			origin[id] = name
		}
	}

	s.mu.Lock()
	s.records = loaded
	s.origin = origin
	s.problems = problems
	s.scanned = stamps
	s.loadedAt = time.Now()
	s.lastCheck = time.Now()
	st := s.status()
	s.mu.Unlock()

	return st, nil
}

// refresh reloads if the directory has changed since the last look.
func (s *FileSource) refresh() {
	s.mu.RLock()
	due := time.Since(s.lastCheck) >= s.minInterval
	s.mu.RUnlock()
	if !due {
		return
	}

	paths, err := s.candidates()
	if err != nil {
		return
	}
	if _, changed := s.changed(paths); !changed {
		s.mu.Lock()
		s.lastCheck = time.Now()
		s.mu.Unlock()
		return
	}
	_, _ = s.reload()
}

// Reload re-reads the directory unconditionally.
func (s *FileSource) Reload() (Status, error) { return s.reload() }

// Status describes what is currently loaded.
func (s *FileSource) Status() Status {
	s.refresh()
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status()
}

// status builds a Status. The caller holds the lock.
func (s *FileSource) status() Status {
	files := map[string]bool{}
	for _, f := range s.origin {
		files[f] = true
	}
	out := Status{
		Dir:      s.dir,
		Farmers:  len(s.records),
		Files:    len(files),
		LoadedAt: s.loadedAt,
		Problems: append([]FileProblem(nil), s.problems...),
	}
	return out
}

// FarmerRecord returns the record for farmerID.
func (s *FileSource) FarmerRecord(_ context.Context, farmerID string) (*FarmerRecord, error) {
	s.refresh()

	s.mu.RLock()
	rec, ok := s.records[farmerID]
	n := len(s.records)
	s.mu.RUnlock()

	if ok {
		return rec, nil
	}
	if n == 0 {
		// The likely case during a trial, and worth distinguishing: nothing
		// has been placed in the directory yet.
		return nil, fmt.Errorf(
			"%w: no farmer records are loaded; place a registry response in %s",
			ErrNotFound, s.dir)
	}
	return nil, fmt.Errorf("%w: %s", ErrNotFound, farmerID)
}

// Fetch is FarmerRecord, as a Registry upstream.
func (s *FileSource) Fetch(ctx context.Context, farmerID string) (*FarmerRecord, error) {
	return s.FarmerRecord(ctx, farmerID)
}

// Name labels records from the directory.
func (s *FileSource) Name() string { return "file" }

// FarmerIDs lists every loaded farmer, sorted for stable output.
func (s *FileSource) FarmerIDs() []string {
	s.refresh()

	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.records))
	for id := range s.records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// OriginOf names the file a farmer's record came from.
func (s *FileSource) OriginOf(farmerID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.origin[farmerID]
}

// FarmerSummary is one row of a farmer listing.
//
// A summary rather than the record, because the console's farmer table was
// fetching every farmer's full record -- parcels, crops, joint owners -- to
// render a name and a village. One request per farmer, and the whole registry
// in the browser's memory. This is what a table row actually needs.
//
// Aadhaar is the last four digits, as it is everywhere else. The mobile is
// carried because an officer searches by it, not because a table shows it.
type FarmerSummary struct {
	FarmerID     string `json:"farmer_id"`
	FarmerName   string `json:"farmer_name"`
	VillageCode  string `json:"village_lgd_code"`
	DistrictCode string `json:"district_lgd_code"`
	MobileNo     string `json:"mobile_no,omitempty"`
	AadhaarLast4 string `json:"aadhaar_last4,omitempty"`

	Parcels      int     `json:"parcels"`
	ExtentHa     float64 `json:"extent_hectares"`
	WithBoundary int     `json:"parcels_with_boundary"`
	Crops        int     `json:"crops"`
}

// Summaries returns every loaded farmer as a row, sorted by name.
//
// Sorted here rather than by the caller so the order is stable across pages:
// a listing that paginates over an unordered map shows some farmers twice and
// others not at all.
func (s *FileSource) Summaries() []FarmerSummary {
	s.refresh()

	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]FarmerSummary, 0, len(s.records))
	for _, rec := range s.records {
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
		out = append(out, sum)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].FarmerName != out[j].FarmerName {
			return out[i].FarmerName < out[j].FarmerName
		}
		// Names are not unique. Falling back to the id keeps the order total,
		// which is what pagination needs.
		return out[i].FarmerID < out[j].FarmerID
	})
	return out
}

// ParcelRow is one parcel, with the farmer it belongs to.
//
// Flattened here rather than in the browser. The parcels page was fetching
// every farmer's whole record and doing this join client-side, which is the
// same N+1 the farmer listing had -- and a district's parcels are several
// times its farmers, so it was the worse of the two.
type ParcelRow struct {
	FarmID       string  `json:"farm_id"`
	SurveyNumber string  `json:"survey_number"`
	FarmerID     string  `json:"farmer_id"`
	FarmerName   string  `json:"farmer_name"`
	VillageCode  string  `json:"village_lgd_code"`
	OwnerExtent  float64 `json:"owner_extent_hectares"`
	HasBoundary  bool    `json:"has_boundary"`
	CropName     string  `json:"crop_name,omitempty"`
	JointOwners  int     `json:"joint_owners"`
}

// Parcels returns every parcel across every loaded farmer, sorted.
func (s *FileSource) Parcels() []ParcelRow {
	s.refresh()

	s.mu.RLock()
	defer s.mu.RUnlock()

	out := []ParcelRow{}
	for _, rec := range s.records {
		for i := range rec.Parcels {
			p := &rec.Parcels[i]
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
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].VillageCode != out[j].VillageCode {
			return out[i].VillageCode < out[j].VillageCode
		}
		// Survey numbers are strings and not always numeric ("453", "12/2").
		// Sorting them as text is what a 7/12 extract does too.
		if out[i].SurveyNumber != out[j].SurveyNumber {
			return out[i].SurveyNumber < out[j].SurveyNumber
		}
		return out[i].FarmID < out[j].FarmID
	})
	return out
}

// Matches reports whether a parcel row satisfies a free-text search.
func (p ParcelRow) Matches(query string) bool {
	if query == "" {
		return true
	}
	q := strings.ToLower(query)
	for _, field := range []string{
		p.SurveyNumber, p.FarmID, p.FarmerName, p.VillageCode, p.CropName,
	} {
		if field != "" && strings.Contains(strings.ToLower(field), q) {
			return true
		}
	}
	return false
}

// Matches reports whether a summary satisfies a free-text search.
//
// Case-insensitive substring over the things an officer arrives with: a name,
// a farmer id, a village code, or a mobile number they have the tail of.
func (f FarmerSummary) Matches(query string) bool {
	if query == "" {
		return true
	}
	q := strings.ToLower(query)
	for _, field := range []string{
		f.FarmerName, f.FarmerID, f.VillageCode, f.MobileNo, f.AadhaarLast4,
	} {
		if field != "" && strings.Contains(strings.ToLower(field), q) {
			return true
		}
	}
	return false
}

// --- parsing ---------------------------------------------------------------

// onSeekEnvelope is AgriStack's async callback wrapper. A captured response
// stores the whole envelope rather than just the payload.
type onSeekEnvelope struct {
	Message struct {
		SearchResponse []struct {
			Data struct {
				RegRecords json.RawMessage `json:"reg_records"`
			} `json:"data"`
		} `json:"search_response"`
	} `json:"message"`
}

// parseRecords pulls farmer records out of whatever shape the file is in.
//
// Three shapes are accepted, because an operator obtaining a record out of
// band will have whichever one their tooling produced, and rejecting two of
// the three would make the difference between a working trial and a confusing
// morning:
//
//   - the full on-seek callback envelope, as captured from the bridge;
//   - a bare reg_records object;
//   - an array of either.
//
// A file that is none of these is reported by name rather than ignored.
func parseRecords(raw []byte) ([]*FarmerRecord, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, fmt.Errorf("is empty")
	}

	if strings.HasPrefix(trimmed, "[") {
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, fmt.Errorf("is not valid JSON: %w", err)
		}
		var out []*FarmerRecord
		for _, item := range items {
			recs, err := parseRecords(item)
			if err != nil {
				return nil, err
			}
			out = append(out, recs...)
		}
		return out, nil
	}

	// The envelope, if this is one. A file that parses as an envelope but
	// carries no search_response is a captured error reply, not a record.
	var env onSeekEnvelope
	if err := json.Unmarshal(raw, &env); err == nil && len(env.Message.SearchResponse) > 0 {
		var out []*FarmerRecord
		for _, sr := range env.Message.SearchResponse {
			if len(sr.Data.RegRecords) == 0 {
				continue
			}
			rec, err := decodeRecord(sr.Data.RegRecords)
			if err != nil {
				return nil, err
			}
			out = append(out, rec)
		}
		if len(out) > 0 {
			return out, nil
		}
		return nil, fmt.Errorf("has a search_response but no reg_records in it")
	}

	rec, err := decodeRecord(raw)
	if err != nil {
		return nil, err
	}
	return []*FarmerRecord{rec}, nil
}

func decodeRecord(raw json.RawMessage) (*FarmerRecord, error) {
	var rec FarmerRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, fmt.Errorf("is not a farmer record: %w", err)
	}
	if rec.Farmer.FarmerID == "" {
		return nil, fmt.Errorf(
			"is not a farmer record: no FarmerData.farmer_id, and no on-seek envelope around it")
	}
	return &rec, nil
}

// --- derived values --------------------------------------------------------

// ParcelByFarmID finds one parcel within a farmer's holding.
func (r *FarmerRecord) ParcelByFarmID(farmID string) (*Parcel, bool) {
	for i := range r.Parcels {
		if r.Parcels[i].FarmID == farmID {
			return &r.Parcels[i], true
		}
	}
	return nil, false
}

// CropsForParcel returns the crop declarations recorded against a parcel.
func (r *FarmerRecord) CropsForParcel(farmID string) []CropSown {
	var out []CropSown
	for _, c := range r.Crops {
		if c.FarmID == farmID {
			out = append(out, c)
		}
	}
	return out
}

// ExtentHectares parses OwnerExtent, converting from acres when the record
// says so. This is the owner's share, not the polygon area.
func (p *Parcel) ExtentHectares() (float64, error) {
	v, err := strconv.ParseFloat(strings.TrimSpace(p.OwnerExtent), 64)
	if err != nil {
		return 0, fmt.Errorf("owner_extent %q: %w", p.OwnerExtent, err)
	}
	if v <= 0 {
		// A zero or negative extent divides into every per-hectare figure
		// downstream. Refusing here is cheaper than an infinity in a report.
		return 0, fmt.Errorf("owner_extent %q is not a positive area", p.OwnerExtent)
	}
	switch strings.ToUpper(strings.TrimSpace(p.AreaUnit)) {
	case "", "HECTARE", "HECTARES", "HA":
		return v, nil
	case "ACRE", "ACRES":
		return v * 0.404686, nil
	case "GUNTHA", "GUNTHAS":
		// One guntha is 1/40 acre. Common on Maharashtra 7/12 extracts.
		return v * 0.404686 / 40, nil
	default:
		return 0, fmt.Errorf("unrecognised area_unit %q", p.AreaUnit)
	}
}

// Area reads the split integer/decimal encoding as integer.decimal.
//
// The AgriStack spec is ambiguous here — it shows {2, "30"} in one place and
// {3, "00006"} in another, without stating whether the decimal part is a
// fraction or a fixed-width field. This reads it as a decimal fraction, which
// matches the first form. Verify against the live registry before a payout
// depends on it: the two readings differ by orders of magnitude.
func (c *CropSown) Area() (float64, error) {
	dec := strings.TrimSpace(c.SownAreaDecimal)
	if dec == "" {
		dec = "0"
	}
	v, err := strconv.ParseFloat(fmt.Sprintf("%d.%s", c.SownAreaInteger, dec), 64)
	if err != nil {
		return 0, fmt.Errorf("sown area %d.%s: %w", c.SownAreaInteger, dec, err)
	}
	if strings.EqualFold(strings.TrimSpace(c.SownAreaUnit), "ACRE") {
		return v * 0.404686, nil
	}
	return v, nil
}

// SowingDateISO converts AgriStack's DD/MM/YYYY to YYYY-MM-DD, which is what
// the satellite engine expects.
func (c *CropSown) SowingDateISO() (string, error) {
	raw := strings.TrimSpace(c.SowingDate)
	if raw == "" {
		return "", fmt.Errorf("no sowing_date recorded")
	}

	// Already ISO. Some exports normalise it, and reformatting an ISO date as
	// though it were DD/MM/YYYY silently transposes the day and the year.
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		return t.Format("2006-01-02"), nil
	}

	t, err := time.Parse("02/01/2006", raw)
	if err != nil {
		return "", fmt.Errorf("sowing_date %q is neither DD/MM/YYYY nor YYYY-MM-DD", raw)
	}
	return t.Format("2006-01-02"), nil
}
