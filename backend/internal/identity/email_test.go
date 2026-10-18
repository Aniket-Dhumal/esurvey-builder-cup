package identity

import (
	"context"
	"errors"
	"testing"
)

func TestAnOfficersLoginIsARealEmailAddress(t *testing.T) {
	for in, want := range map[string]string{
		"aniket@mindstrix.com":         "aniket@mindstrix.com",
		"  R.Deshmukh@NIC.in ":         "r.deshmukh@nic.in",
		"collector.solapur@mah.gov.in": "collector.solapur@mah.gov.in",
		"first+tag@sub.domain.co.in":   "first+tag@sub.domain.co.in",
		"o'brien@agri.gov.in":          "o'brien@agri.gov.in",
	} {
		got, err := NormalizeEmail(in)
		if err != nil {
			t.Errorf("%q was refused: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%q normalised to %q, want %q", in, got, want)
		}
	}

	for _, in := range []string{
		"",
		"admin",                      // the old usernames
		"dm.solapur",                 // no @
		"a@b",                        // no dot in the domain
		"two@@nic.in",                // two @
		"a@b@nic.in",                 // two @
		"@nic.in",                    // no local part
		"name@",                      // no domain
		".lead@nic.in",               // leading dot
		"trail.@nic.in",              // trailing dot
		"dou..ble@nic.in",            // double dot
		"space in@nic.in",            // space
		"name@nic..in",               // empty label
		"name@-nic.in",               // label starts with a hyphen
		"name@nic-.in",               // label ends with a hyphen
		"name@nic.i",                 // one-letter suffix
		"name@nic.123",               // numeric suffix
		"verify.admin@esurvey.local", // reserved suffix
		"someone@server.localhost",
		"officer@example.com", // reserved domain
		"x@corp.internal",
		"quoted\"name@nic.in",
	} {
		if got, err := NormalizeEmail(in); err == nil {
			t.Errorf("%q was accepted as %q", in, got)
		} else if !errors.Is(err, ErrInvalidEmail) {
			t.Errorf("%q: error %v is not ErrInvalidEmail", in, err)
		}
	}
}

// No path into the store, and no row written around it, can give an officer a
// login that is not their email address.
func TestTheStoreAndTheTableRefuseANonEmailLogin(t *testing.T) {
	store, pool := testIdentity(t)
	ctx := context.Background()

	if _, err := store.CreateOfficer(ctx, NewOfficer{
		Username: "tst.admin", FullName: "Not An Email",
		PasswordHash: "x", MustChangePassword: true,
	}); !errors.Is(err, ErrInvalidEmail) {
		t.Errorf("the store created an officer with login tst.admin: %v", err)
	}

	if _, err := store.CreateOfficer(ctx, NewOfficer{
		Username: "tst.one@esurvey-test.in", Email: "tst.two@esurvey-test.in",
		FullName: "Two Addresses", PasswordHash: "x", MustChangePassword: true,
	}); !errors.Is(err, ErrInvalidEmail) {
		t.Errorf("the store accepted a login different from the email: %v", err)
	}

	u, err := store.CreateOfficer(ctx, NewOfficer{
		Email: "  TST.Valid@ESURVEY-TEST.in ", FullName: "Valid",
		PasswordHash: "x", MustChangePassword: true,
	})
	if err != nil {
		t.Fatalf("a valid address was refused: %v", err)
	}
	if u.Username != "tst.valid@esurvey-test.in" || u.Email != u.Username {
		t.Errorf("stored login %q, email %q; want both the normalised address",
			u.Username, u.Email)
	}

	// Around the store, straight into the table.
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (kind, full_name, username, password_hash)
		VALUES ('officer', 'Raw Insert', 'tst.raw', 'x')`); err == nil {
		t.Error("the table accepted an officer whose login is not an email")
	}
}
