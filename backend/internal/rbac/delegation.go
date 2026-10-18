package rbac

import "slices"

// Who may create whom.
//
// # Why this file exists
//
// user:create used to sit with the system administrator alone, and the reason
// was written into the matrix: whoever can mint an officer account must not
// also be able to approve a claim with it, or account creation becomes an
// approval path with no second signature on it. A Tehsildar who can create a
// Talathi can create one, post it to a village, file and verify a report
// through it, and then approve that report themselves at tehsil level.
//
// That control has been deliberately given up: a district cannot wait on a
// system administrator to add a Talathi in the middle of a calamity, and the
// operators of this platform decided the delay costs more than the risk. The
// audit trail is now the control -- every creation, every posting and every
// status change is recorded against the officer who made it.
//
// # What is not given up
//
// Two limits remain, because neither of them was what stood in the way.
//
// The first is rank: an officer may create only roles beneath their own. A
// Tehsildar minting a District Magistrate would not be delegation, it would be
// promotion, and it would let the whole ladder be climbed from its second
// rung.
//
// The second is place, enforced at the call site rather than here: the new
// officer's posting must lie inside the creating officer's own. A Tehsildar
// staffs their tehsil, not the one next door.
//
// Together these mean the power an officer gains is exactly the power to staff
// the ground they are already answerable for.

// delegable is the closed set of roles each role may create and post.
//
// A role absent from this map may create nobody.
var delegable = map[Role][]Role{
	// The product administrator onboards a state by appointing its State
	// Officer, and adds other product administrators. Nothing below: a
	// district, a taluka and a village are the client's own to staff, and the
	// operator of the product has no business choosing who decides a claim.
	RoleSystemAdmin: {RoleStateOfficer, RoleSystemAdmin},

	// The state appoints each district's authority, and nothing below it.
	//
	// It was absent, on the reasoning that the state's role is oversight. But
	// a district's authority then had to come from the system administrator,
	// an account with no place in the ladder, when the state is who a
	// Collector answers to. District Magistrates only: the district staffs
	// its own tehsils and villages, and a state officer reaching past the
	// Collector to post a Talathi would bypass the officer answerable for
	// that village. Place still binds -- the posting must be inside the
	// state officer's own state.
	RoleStateOfficer: {RoleDistrictMagistrate},

	RoleDistrictMagistrate: {
		RoleTehsildar, RoleMandalOfficer, RoleTalathi, RoleKrushiSahayak,
	},

	RoleTehsildar: {
		RoleMandalOfficer, RoleTalathi, RoleKrushiSahayak,
	},
}

// CanAssign reports whether an officer holding actor may create, post or
// suspend an officer holding target.
func CanAssign(actor, target Role) bool {
	return slices.Contains(delegable[actor], target)
}

// AssignableBy is every role the principal may create, across all the roles
// they hold. Ordered by AssignableRoles so the console's dropdown is stable
// rather than in map order.
//
// The empty result is the ordinary case for most officers and means the
// account form is not offered to them at all.
func (p *Principal) AssignableBy() []Role {
	if p == nil || p.MustChangePassword {
		return nil
	}
	var out []Role
	for _, want := range AssignableRoles {
		for _, g := range p.Grants {
			if CanAssign(g.Role, want) {
				out = append(out, want)
				break
			}
		}
	}
	return out
}

// CanAssignRole reports whether the principal may act on accounts of the given
// role. Convenience over AssignableBy for a single check.
func (p *Principal) CanAssignRole(target Role) bool {
	if p == nil || p.MustChangePassword {
		return false
	}
	for _, g := range p.Grants {
		if CanAssign(g.Role, target) {
			return true
		}
	}
	return false
}
