package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/agribid/esurvey-platform/backend/internal/identity"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// The two limits on an officer managing other officers' accounts.
//
// user:create sat with the system administrator alone until it was given to
// District Magistrates and Tehsildars, so that a district could staff a
// village during a calamity without waiting on an administrator. What that
// gave up is written out in rbac/delegation.go; what it did not give up is
// enforced here.
//
// Rank: an officer creates, posts and suspends only roles beneath their own.
// Place: the posting must lie inside the officer's own.
//
// Both are checked on the way in, and both are checked again against the
// account's *existing* postings before anything is done to an account that
// already exists -- otherwise "a Tehsildar may manage a Talathi" would let a
// Tehsildar suspend the Collector's Talathi in the next tehsil, or worse,
// suspend an account that also holds a District Magistrate posting.

var errNotDelegable = errors.New("delegation refused")

// mayPost checks that the actor may create the given role in the given place.
func (s *Server) mayPost(
	ctx context.Context, actor *rbac.Principal, role rbac.Role, jurisdictionID string,
) error {
	if !actor.CanAssignRole(role) {
		return fmt.Errorf("%w: your role cannot create a %s", errNotDelegable, role)
	}
	// The product administrator holds no posting and appoints State Officers
	// to any state and other administrators to none; rank, checked above, is
	// its only limit.
	if actor.HasRole(rbac.RoleSystemAdmin) {
		return nil
	}
	// Placeless roles are not creatable by anyone but the administrator, which
	// the rank check above has already settled.
	if jurisdictionID == "" {
		return fmt.Errorf("%w: only an administrator creates an unposted account",
			errNotDelegable)
	}

	within, err := s.Identity.WithinAny(ctx, jurisdictionID, actor.Scope().JurisdictionIDs)
	if err != nil {
		return err
	}
	if !within {
		return fmt.Errorf("%w: that posting is outside your own", errNotDelegable)
	}
	return nil
}

// mayManage checks that the actor may act on an account that already exists.
//
// Every posting the account holds has to be one the actor could have made
// themselves. Checking only the posting being changed would let a Tehsildar
// suspend an account that is a Talathi in their tehsil *and* a Tehsildar
// somewhere else, and the suspension would take the second posting with it.
func (s *Server) mayManage(
	ctx context.Context, actor *rbac.Principal, target *identity.User,
) error {
	if target.ID == actor.UserID {
		// Not a delegation question -- an officer editing their own postings
		// is an officer promoting themselves.
		return fmt.Errorf("%w: an account cannot manage itself", errNotDelegable)
	}
	if len(target.Grants) == 0 {
		return fmt.Errorf("%w: that account holds no posting you can reach",
			errNotDelegable)
	}

	// The product administrator manages State Officers and administrators
	// anywhere, and no other account: a Collector or a Talathi belongs to the
	// state that appointed them.
	if actor.HasRole(rbac.RoleSystemAdmin) {
		for _, g := range target.Grants {
			if !actor.CanAssignRole(g.Role) {
				return fmt.Errorf("%w: that account holds a %s posting, which the "+
					"product administrator does not manage", errNotDelegable, g.Role)
			}
		}
		return nil
	}

	mine := actor.Scope().JurisdictionIDs
	for _, g := range target.Grants {
		if !actor.CanAssignRole(g.Role) {
			return fmt.Errorf("%w: that account holds a %s posting, which your role cannot manage",
				errNotDelegable, g.Role)
		}
		within, err := s.Identity.WithinAny(ctx, g.JurisdictionID, mine)
		if err != nil {
			return err
		}
		if !within {
			return fmt.Errorf("%w: that account is posted outside your own area",
				errNotDelegable)
		}
	}
	return nil
}

// writeDelegationError answers a refused delegation.
//
// 403 with the reason, because unlike most authorization refusals this one is
// safe and useful to explain: the caller already holds user:create, and "you
// cannot create a District Magistrate" tells them the shape of their own
// authority rather than anything about the account they aimed at.
func writeDelegationError(w http.ResponseWriter, err error) bool {
	if !errors.Is(err, errNotDelegable) {
		return false
	}
	writeError(w, http.StatusForbidden, "NOT_DELEGABLE", err.Error())
	return true
}
