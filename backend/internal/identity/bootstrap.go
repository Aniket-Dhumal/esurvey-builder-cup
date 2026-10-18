package identity

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// Bootstrap creates the first administrator on an empty deployment.
//
// Something has to break the circularity: accounts are created by an
// administrator, and the first administrator has no one to create them. The
// alternatives are worse than this one. A default account shipped in a
// migration is a published credential; a setup wizard on an open endpoint is
// a race with whoever finds the host first.
//
// So: it runs only when the user table is empty, takes the credential from
// the environment rather than from a request, and the account it creates
// cannot do anything until its password has been replaced -- the hash it
// starts with belongs to whoever typed it into a deployment file, not to the
// person who will use it.
func Bootstrap(
	ctx context.Context, store *Store, log *slog.Logger,
	username, passwordHash string,
) error {
	n, err := store.Count(ctx)
	if err != nil {
		return fmt.Errorf("counting accounts: %w", err)
	}
	if n > 0 {
		return nil
	}

	if username == "" || passwordHash == "" {
		return errors.New(
			"no accounts exist and no bootstrap administrator is configured; " +
				"set ESURVEY_BOOTSTRAP_ADMIN_USERNAME and " +
				"ESURVEY_BOOTSTRAP_ADMIN_PASSWORD, or the deployment cannot be " +
				"signed in to")
	}

	user, err := store.CreateOfficer(ctx, NewOfficer{
		Username:           username,
		FullName:           "Bootstrap Administrator",
		PasswordHash:       passwordHash,
		MustChangePassword: true,
	})
	if err != nil {
		return fmt.Errorf("creating bootstrap administrator: %w", err)
	}

	// system_admin carries no authority over claims; see the matrix in
	// internal/rbac. The first account can create officers and read the audit
	// log, and cannot approve anything.
	if err := store.GrantRole(ctx, user.ID, rbac.RoleSystemAdmin, "", ""); err != nil {
		return fmt.Errorf("granting bootstrap role: %w", err)
	}

	log.Warn("bootstrap administrator created",
		"username", username,
		"user_id", user.ID,
		"note", "this account must set its own password before it can do anything; "+
			"remove ESURVEY_BOOTSTRAP_ADMIN_PASSWORD from the environment once that is done")

	return nil
}
