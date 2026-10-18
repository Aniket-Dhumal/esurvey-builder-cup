package rbac

import (
	"context"
	"slices"
	"testing"
)

func officer(role Role, jurisdictionID, code string) *Principal {
	return &Principal{
		UserID: "u1",
		Kind:   KindOfficer,
		Grants: []Grant{{
			Role:             role,
			JurisdictionID:   jurisdictionID,
			JurisdictionCode: code,
		}},
	}
}

// Separation of duties. Whoever can mint an officer account must not also be
// able to decide a claim with it, or account creation becomes an approval
// path with no second signature on it.
func TestSystemAdminHoldsNoAuthorityOverClaims(t *testing.T) {
	forbidden := []Permission{
		PermAssessmentCreate, PermAssessmentRead, PermAssessmentReview,
		PermAssessmentApprove, PermAssessmentEscalate,
		PermFarmerRead, PermParcelRead,
	}
	for _, p := range forbidden {
		if RoleCan(RoleSystemAdmin, p) {
			t.Errorf("system_admin must not carry %s", p)
		}
	}

	// And it must keep the powers it exists for.
	for _, p := range []Permission{PermUserCreate, PermRoleGrant, PermAuditRead} {
		if !RoleCan(RoleSystemAdmin, p) {
			t.Errorf("system_admin should carry %s", p)
		}
	}
}

// The joint panchnama is signed by the revenue side and the agriculture side
// together. A model where either alone can close a review is not the process
// these officers follow.
func TestBothVillageOfficersCanReview(t *testing.T) {
	for _, r := range []Role{RoleTalathi, RoleKrushiSahayak} {
		if !RoleCan(r, PermAssessmentReview) {
			t.Errorf("%s should carry assessment:review", r)
		}
		if RoleCan(r, PermAssessmentApprove) {
			t.Errorf("%s must not carry assessment:approve; approval sits above the village", r)
		}
	}
}

func TestOnlyTalukaAndAboveApprove(t *testing.T) {
	canApprove := []Role{RoleTehsildar, RoleDistrictMagistrate}
	for _, r := range AllRoles {
		got := RoleCan(r, PermAssessmentApprove)
		want := slices.Contains(canApprove, r)
		if got != want {
			t.Errorf("%s approve = %v, want %v", r, got, want)
		}
	}
}

func TestFarmerSeesOnlyTheirOwnSide(t *testing.T) {
	f := &Principal{UserID: "u2", Kind: KindFarmer, FarmerID: "56567763672",
		Grants: []Grant{{Role: RoleFarmer}}}

	if !f.Can(PermClaimSubmit) {
		t.Error("a farmer should be able to submit a claim")
	}
	for _, p := range []Permission{
		PermAssessmentCreate, PermAssessmentReview, PermAssessmentApprove,
		PermUserCreate, PermAuditRead,
	} {
		if f.Can(p) {
			t.Errorf("a farmer must not carry %s", p)
		}
	}

	// Their scope is an identity, never a place.
	scope := f.Scope()
	if scope.FarmerID != "56567763672" {
		t.Errorf("farmer scope = %+v, want the registry id", scope)
	}
	if len(scope.JurisdictionIDs) != 0 {
		t.Errorf("a farmer must not carry a jurisdiction scope: %+v", scope.JurisdictionIDs)
	}
}

// The bootstrap password was typed by someone other than the account holder,
// and must not be a working credential for anything but replacing itself.
func TestMustChangePasswordHoldsNothing(t *testing.T) {
	p := officer(RoleDistrictMagistrate, "j1", "496")
	p.MustChangePassword = true

	for _, perm := range Permissions(RoleDistrictMagistrate) {
		if p.Can(perm) {
			t.Fatalf("an account owing a password change still carried %s", perm)
		}
	}
	if !p.RestrictedToPasswordChange() {
		t.Error("the restriction should be visible to the password-change route")
	}
}

func TestScopeCollectsEveryPosting(t *testing.T) {
	// Additional charge of a neighbouring circle: two live grants, one person.
	p := &Principal{
		UserID: "u3", Kind: KindOfficer,
		Grants: []Grant{
			{Role: RoleMandalOfficer, JurisdictionID: "j-circle-1", JurisdictionCode: "C-1"},
			{Role: RoleMandalOfficer, JurisdictionID: "j-circle-2", JurisdictionCode: "C-2"},
		},
	}

	scope := p.Scope()
	if len(scope.JurisdictionIDs) != 2 {
		t.Fatalf("scope = %+v, want both postings", scope.JurisdictionIDs)
	}
	if codes := p.JurisdictionCodes(); len(codes) != 2 {
		t.Errorf("codes = %v, want both", codes)
	}
}

func TestScopeDeduplicates(t *testing.T) {
	p := &Principal{
		UserID: "u4", Kind: KindOfficer,
		Grants: []Grant{
			{Role: RoleTalathi, JurisdictionID: "j1", JurisdictionCode: "562111"},
			{Role: RoleKrushiSahayak, JurisdictionID: "j1", JurisdictionCode: "562111"},
		},
	}
	if got := p.Scope().JurisdictionIDs; len(got) != 1 {
		t.Errorf("scope = %v, want one distinct jurisdiction", got)
	}
}

// An empty scope must admit nothing. The repositories rely on this to refuse
// rather than fall through to an unfiltered query.
func TestEmptyScopeAdmitsNothing(t *testing.T) {
	if !(Scope{}).Empty() {
		t.Error("a zero scope should report empty")
	}
	if (&Principal{Kind: KindOfficer}).Scope().Empty() != true {
		t.Error("an officer with no posting should have an empty scope")
	}
	if (Scope{FarmerID: "x"}).Empty() {
		t.Error("a farmer scope is not empty")
	}
}

func TestNilPrincipalCanDoNothing(t *testing.T) {
	var p *Principal
	if p.Can(PermAssessmentRead) {
		t.Error("a nil principal must not carry permissions")
	}
	if !p.Scope().Empty() {
		t.Error("a nil principal must have an empty scope")
	}
	if err := Require(context.Background(), PermAssessmentRead); err != ErrUnauthenticated {
		t.Errorf("Require on an anonymous context = %v, want ErrUnauthenticated", err)
	}
}

func TestRequireUsesTheContextPrincipal(t *testing.T) {
	ctx := WithPrincipal(context.Background(), officer(RoleTehsildar, "j1", "4246"))

	if err := Require(ctx, PermAssessmentApprove); err != nil {
		t.Errorf("a Tehsildar should hold assessment:approve: %v", err)
	}
	// A Tehsildar now holds user:create -- see delegation.go for the control
	// that was given up to put it there. What replaces it is tested in
	// TestDelegationStopsAtRank: the permission is real, and its reach is not
	// the whole account roll.
	if err := Require(ctx, PermUserCreate); err != nil {
		t.Errorf("a Tehsildar should hold user:create: %v", err)
	}
}

// The limit that replaced separation of duties: an officer creates downward,
// never sideways and never up.
//
// This is the test that matters now. Without it, "a Tehsildar can add
// officers" and "a Tehsildar can add a District Magistrate" are one line
// apart, and the second one hands the whole ladder to its second rung.
func TestDelegationStopsAtRank(t *testing.T) {
	cases := []struct {
		actor, target Role
		want          bool
	}{
		{RoleTehsildar, RoleTalathi, true},
		{RoleTehsildar, RoleKrushiSahayak, true},
		{RoleTehsildar, RoleMandalOfficer, true},
		// Sideways: a Tehsildar staffing a peer is not delegation.
		{RoleTehsildar, RoleTehsildar, false},
		// Upward, and the one that would matter most.
		{RoleTehsildar, RoleDistrictMagistrate, false},
		{RoleTehsildar, RoleSystemAdmin, false},

		{RoleDistrictMagistrate, RoleTehsildar, true},
		{RoleDistrictMagistrate, RoleTalathi, true},
		{RoleDistrictMagistrate, RoleDistrictMagistrate, false},
		{RoleDistrictMagistrate, RoleSystemAdmin, false},

		// The product administrator onboards states and adds its own peers,
		// and staffs nothing a state appoints itself.
		{RoleSystemAdmin, RoleStateOfficer, true},
		{RoleSystemAdmin, RoleSystemAdmin, true},
		{RoleSystemAdmin, RoleDistrictMagistrate, false},
		{RoleSystemAdmin, RoleTalathi, false},

		// Roles with no ground to staff delegate to nobody.
		{RoleMandalOfficer, RoleTalathi, false},
		{RoleTalathi, RoleTalathi, false},
		{RoleFarmer, RoleTalathi, false},

		// The state appoints each district's authority -- and only that. The
		// district staffs itself beneath.
		{RoleStateOfficer, RoleDistrictMagistrate, true},
		{RoleStateOfficer, RoleTehsildar, false},
		{RoleStateOfficer, RoleTalathi, false},
		{RoleStateOfficer, RoleStateOfficer, false},
		{RoleStateOfficer, RoleSystemAdmin, false},
	}
	for _, c := range cases {
		if got := CanAssign(c.actor, c.target); got != c.want {
			t.Errorf("CanAssign(%s, %s) = %v, want %v",
				c.actor, c.target, got, c.want)
		}
	}
}

// A farmer's role is never assignable however senior the caller: farmer
// accounts come from registry enrolment, not from somebody deciding.
func TestFarmerIsNeverDelegable(t *testing.T) {
	for _, actor := range AllRoles {
		if CanAssign(actor, RoleFarmer) {
			t.Errorf("%s may assign RoleFarmer; farmer accounts come from enrolment", actor)
		}
	}
}

// An officer who has not yet set their own password holds nothing -- the same
// rule Can() enforces, which AssignableBy has to honour or the account form
// appears for the bootstrap administrator before it is safe to use.
func TestUnsetPasswordDelegatesNothing(t *testing.T) {
	p := officer(RoleDistrictMagistrate, "j1", "496")
	p.MustChangePassword = true
	if got := p.AssignableBy(); len(got) != 0 {
		t.Errorf("AssignableBy on an unset password = %v, want none", got)
	}
	if p.CanAssignRole(RoleTalathi) {
		t.Error("CanAssignRole on an unset password should be false")
	}
}

// Every role in the enum must appear in the matrix. A role added to one and
// not the other fails closed, which is safe but silent -- this makes it loud.
func TestEveryRoleHasPermissions(t *testing.T) {
	for _, r := range AllRoles {
		if len(Permissions(r)) == 0 {
			t.Errorf("role %s carries no permissions; is it missing from the matrix?", r)
		}
		if !r.Valid() {
			t.Errorf("role %s is in AllRoles but reports invalid", r)
		}
	}
	if Role("commissioner").Valid() {
		t.Error("an unknown role reported valid")
	}
}

// A farmer role must not be grantable through the admin API: farmer accounts
// come from registry enrolment, not from an administrator's judgement.
func TestFarmerIsNotAssignable(t *testing.T) {
	if Assignable(RoleFarmer) {
		t.Error("farmer must not be assignable through the admin API")
	}
	for _, r := range AssignableRoles {
		if !r.Valid() {
			t.Errorf("assignable role %s is not a known role", r)
		}
	}
}

func TestPermissionsAreCopied(t *testing.T) {
	got := Permissions(RoleTalathi)
	if len(got) == 0 {
		t.Fatal("no permissions returned")
	}
	got[0] = Permission("mutated")

	if slices.Contains(Permissions(RoleTalathi), Permission("mutated")) {
		t.Fatal("the matrix was mutated through a returned slice")
	}
}

func TestPrimaryRoleFollowsTheHierarchy(t *testing.T) {
	p := &Principal{
		Kind: KindOfficer,
		Grants: []Grant{
			{Role: RoleTalathi, JurisdictionID: "j1"},
			{Role: RoleTehsildar, JurisdictionID: "j2"},
		},
	}
	if got := p.PrimaryRole(); got != RoleTehsildar {
		t.Errorf("primary role = %s, want the senior posting", got)
	}
}

// Only the District Magistrate declares a calamity. The process the platform
// mirrors is that the district issues the notification; if that ever changes
// to allow state-level declaration it should change here, visibly, with this
// test updated alongside it.
func TestOnlyTheCollectorDeclaresAnEvent(t *testing.T) {
	for _, r := range AllRoles {
		declares := RoleCan(r, PermEventCreate) || RoleCan(r, PermEventDeclare)
		if declares && r != RoleDistrictMagistrate {
			t.Errorf("%s can declare a survey event; only the Collector should", r)
		}
	}
	for _, p := range []Permission{
		PermEventCreate, PermEventRead, PermEventDeclare, PermEventClose,
	} {
		if !RoleCan(RoleDistrictMagistrate, p) {
			t.Errorf("the Collector should carry %s", p)
		}
	}
}

// Everyone who touches a claim must be able to read the event it hangs off --
// including the farmer, whose deadline it sets.
func TestEveryClaimRoleCanReadEvents(t *testing.T) {
	for _, r := range []Role{
		RoleStateOfficer, RoleDistrictMagistrate, RoleTehsildar,
		RoleMandalOfficer, RoleTalathi, RoleKrushiSahayak, RoleFarmer,
	} {
		if !RoleCan(r, PermEventRead) {
			t.Errorf("%s cannot read survey events", r)
		}
	}
	// The account administrator has no business in a claim, event included.
	if RoleCan(RoleSystemAdmin, PermEventRead) {
		t.Error("system_admin should not read survey events")
	}
}
