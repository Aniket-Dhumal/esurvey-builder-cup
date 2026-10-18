package rbac

import (
	"context"
	"errors"
	"slices"
)

// Level is a rung of the administrative tree.
type Level string

const (
	LevelState       Level = "state"
	LevelDistrict    Level = "district"
	LevelSubDistrict Level = "sub_district"
	LevelCircle      Level = "circle"
	LevelVillage     Level = "village"
)

// Kind distinguishes the two ways an account authenticates.
type Kind string

const (
	KindOfficer Kind = "officer"
	KindFarmer  Kind = "farmer"
)

// Grant is one role held in one place.
type Grant struct {
	Role Role `json:"role"`

	// Jurisdiction is empty for a farmer, whose authority is over their own
	// records rather than over a place.
	JurisdictionID    string `json:"jurisdiction_id,omitempty"`
	JurisdictionCode  string `json:"jurisdiction_code,omitempty"`
	JurisdictionLevel Level  `json:"jurisdiction_level,omitempty"`
	JurisdictionName  string `json:"jurisdiction_name,omitempty"`
}

// Principal is the authenticated caller.
//
// It is assembled from the access token alone. That is the point of putting
// grants in the token: an authorization decision costs no database round trip,
// and the cost of that choice -- a revoked grant stays usable until the access
// token expires -- is bounded by keeping access tokens short and by revoking
// the refresh token immediately, which caps the window at one access lifetime.
type Principal struct {
	UserID    string `json:"user_id"`
	Kind      Kind   `json:"kind"`
	Name      string `json:"name"`
	SessionID string `json:"session_id"`

	// FarmerID is the AgriStack registry identifier, for farmer accounts.
	FarmerID string `json:"farmer_id,omitempty"`

	Grants []Grant `json:"grants"`

	// MustChangePassword narrows what an officer may do until they have set a
	// password of their own; see RestrictedToPasswordChange.
	MustChangePassword bool `json:"must_change_password,omitempty"`
}

// Can reports whether any of the principal's roles carries the permission.
//
// This answers "what", never "where". Every call site that touches a specific
// parcel, farmer or assessment must also pass a scope check -- Scope() and
// the repository filters exist so that this is the path of least resistance
// rather than an extra step somebody skips.
func (p *Principal) Can(perm Permission) bool {
	if p == nil {
		return false
	}
	// An officer who has not yet set their own password holds nothing. The
	// bootstrap administrator ships with a password an operator typed into an
	// environment file, and it must not be usable for real work.
	if p.MustChangePassword {
		return false
	}
	for _, g := range p.Grants {
		if RoleCan(g.Role, perm) {
			return true
		}
	}
	return false
}

// HasRole reports whether the principal holds a role anywhere.
func (p *Principal) HasRole(r Role) bool {
	if p == nil {
		return false
	}
	for _, g := range p.Grants {
		if g.Role == r {
			return true
		}
	}
	return false
}

// Roles lists the distinct roles held.
func (p *Principal) Roles() []Role {
	if p == nil {
		return nil
	}
	var out []Role
	for _, g := range p.Grants {
		if !slices.Contains(out, g.Role) {
			out = append(out, g.Role)
		}
	}
	return out
}

// IsFarmer reports whether this is a farmer account.
func (p *Principal) IsFarmer() bool { return p != nil && p.Kind == KindFarmer }

// Scope is the "where" half of an authorization decision, in the form a
// repository can push into SQL.
//
// Scoping belongs in the query. Loading every assessment and filtering in Go
// is one forgotten early return away from a Talathi reading the district, and
// it does not survive the first page of results either.
type Scope struct {
	// JurisdictionIDs are the nodes the principal holds a posting in. A row
	// is in scope when its village is one of these or sits beneath one.
	JurisdictionIDs []string

	// FarmerID restricts to one registry identity. Set for farmers, who see
	// their own claims and nothing else.
	FarmerID string
}

// Empty reports whether the scope admits nothing at all.
func (s Scope) Empty() bool {
	return len(s.JurisdictionIDs) == 0 && s.FarmerID == ""
}

// Scope derives the principal's reach.
func (p *Principal) Scope() Scope {
	if p == nil {
		return Scope{}
	}
	if p.Kind == KindFarmer {
		return Scope{FarmerID: p.FarmerID}
	}

	var ids []string
	for _, g := range p.Grants {
		if g.JurisdictionID != "" && !slices.Contains(ids, g.JurisdictionID) {
			ids = append(ids, g.JurisdictionID)
		}
	}
	return Scope{JurisdictionIDs: ids}
}

// JurisdictionCodes lists the codes the principal is posted to, for display
// and for audit records.
func (p *Principal) JurisdictionCodes() []string {
	if p == nil {
		return nil
	}
	var out []string
	for _, g := range p.Grants {
		if g.JurisdictionCode != "" && !slices.Contains(out, g.JurisdictionCode) {
			out = append(out, g.JurisdictionCode)
		}
	}
	return out
}

// PrimaryRole is the highest posting held, for logging and display. Ordering
// follows the administrative hierarchy rather than permission counts.
func (p *Principal) PrimaryRole() Role {
	order := []Role{
		RoleSystemAdmin, RoleStateOfficer, RoleDistrictMagistrate,
		RoleTehsildar, RoleMandalOfficer, RoleTalathi, RoleKrushiSahayak,
		RoleFarmer,
	}
	for _, r := range order {
		if p.HasRole(r) {
			return r
		}
	}
	return ""
}

// Errors returned by authorization checks. They are distinguished because
// they mean different things to a caller: one is "sign in", the other is
// "you are signed in and the answer is still no".
var (
	ErrUnauthenticated = errors.New("no authenticated principal")
	ErrForbidden       = errors.New("principal lacks the required permission")
	ErrOutOfScope      = errors.New("resource is outside the principal's jurisdiction")
)

type ctxKey struct{}

// WithPrincipal returns a context carrying the principal.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext returns the principal, or nil when the request is anonymous.
func FromContext(ctx context.Context) *Principal {
	p, _ := ctx.Value(ctxKey{}).(*Principal)
	return p
}

// Require checks a permission against the context's principal.
func Require(ctx context.Context, perm Permission) error {
	p := FromContext(ctx)
	if p == nil {
		return ErrUnauthenticated
	}
	if !p.Can(perm) {
		return ErrForbidden
	}
	return nil
}

// RestrictedToPasswordChange reports whether the principal may do nothing but
// set a new password. Kept separate from Can so that the password-change
// endpoint itself can admit them.
func (p *Principal) RestrictedToPasswordChange() bool {
	return p != nil && p.MustChangePassword
}
