// Package identity is the durable half of who someone is: accounts, the
// administrative tree they are posted to, and the grants that connect the two.
//
// Transient security state -- refresh tokens, one-time codes, lockouts --
// deliberately lives in Redis instead; see internal/auth. The division is
// that anything whose loss would cost an entitlement is here, and anything
// whose loss costs a sign-in is not.
package identity

import (
	"errors"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// Store errors.
var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("already exists")
	// ErrInactive covers suspension and deactivation. The caller is told only
	// that sign-in is refused; which of the two it was is an internal detail
	// that an attacker probing accounts does not need.
	ErrInactive = errors.New("account is not active")
	// ErrNoPosting is an officer created with no place to work. Every officer
	// role in this system is meaningless without one -- see the
	// role_has_jurisdiction constraint -- so it is refused before the
	// database has to.
	ErrNoPosting = errors.New("an officer must be posted somewhere")
)

// Status is an account's lifecycle state.
type Status string

const (
	StatusActive      Status = "active"
	StatusSuspended   Status = "suspended"
	StatusDeactivated Status = "deactivated"
)

// User is one account.
type User struct {
	ID     string    `json:"id"`
	Kind   rbac.Kind `json:"kind"`
	Status Status    `json:"status"`

	FullName string `json:"full_name"`
	Email    string `json:"email,omitempty"`

	// Officer credentials.
	Username           string `json:"username,omitempty"`
	PasswordHash       string `json:"-"`
	MustChangePassword bool   `json:"must_change_password"`

	// Farmer credentials and registry link.
	MobileE164        string `json:"mobile_e164,omitempty"`
	AgriStackFarmerID string `json:"agristack_farmer_id,omitempty"`
	// AadhaarLast4 is the only part of an Aadhaar this system ever displays.
	AadhaarLast4 string `json:"aadhaar_last4,omitempty"`
	AadhaarHash  string `json:"-"`

	PasswordChangedAt *time.Time `json:"password_changed_at,omitempty"`
	LastLoginAt       *time.Time `json:"last_login_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`

	Grants []rbac.Grant `json:"grants"`
}

// Active reports whether the account may sign in.
func (u *User) Active() bool { return u.Status == StatusActive }

// Principal projects the account into an authorization principal.
func (u *User) Principal(sessionID string) *rbac.Principal {
	return &rbac.Principal{
		UserID:             u.ID,
		Kind:               u.Kind,
		Name:               u.FullName,
		SessionID:          sessionID,
		FarmerID:           u.AgriStackFarmerID,
		Grants:             u.Grants,
		MustChangePassword: u.MustChangePassword,
	}
}

// NewOfficer is the input for creating an officer account.
type NewOfficer struct {
	Username     string
	FullName     string
	Email        string
	PasswordHash string
	// MustChangePassword is true for every administrator-created account: the
	// password was chosen by someone other than its owner and has already
	// been seen by them.
	MustChangePassword bool
	CreatedBy          string
}

// NewFarmer is the input for enrolling a farmer account.
//
// Enrolment is against the registry, not an administrator's judgement: the
// farmer id, Aadhaar hash and handset all come from AgriStack.
type NewFarmer struct {
	FullName          string
	MobileE164        string
	AgriStackFarmerID string
	AadhaarHash       string
	AadhaarLast4      string
}

// Jurisdiction is one node of the administrative tree.
type Jurisdiction struct {
	ID       string     `json:"id"`
	Level    rbac.Level `json:"level"`
	Code     string     `json:"code"`
	Name     string     `json:"name"`
	ParentID string     `json:"parent_id,omitempty"`
}
