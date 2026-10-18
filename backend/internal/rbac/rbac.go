// Package rbac is the authorization policy.
//
// Authorization here has two axes and both must pass:
//
//	what   -- the permissions the principal's roles carry
//	where  -- the jurisdiction those roles were granted in
//
// A Tehsildar holds assessment:approve inside their own taluka and nowhere
// else. Neither half of that sentence means anything on its own, so no check
// in this system consults one without the other.
//
// # Why the matrix lives in Go
//
// Only role *grants* are stored in the database. What a role may do is
// compiled in, because it is a policy artifact: a change to it should arrive
// as a reviewed diff with a test beside it, not as an UPDATE that nobody saw.
// It also removes a standing privilege-escalation path -- there is no row
// anyone can edit to give themselves a permission.
package rbac

import "slices"

// Role is a posting in the administrative hierarchy.
type Role string

const (
	// RoleSystemAdmin administers accounts. It deliberately holds no
	// authority over claims; see the matrix below.
	RoleSystemAdmin Role = "system_admin"

	RoleStateOfficer       Role = "state_officer"
	RoleDistrictMagistrate Role = "district_magistrate"
	RoleTehsildar          Role = "tehsildar"
	RoleMandalOfficer      Role = "mandal_officer"
	RoleTalathi            Role = "talathi"
	RoleKrushiSahayak      Role = "krushi_sahayak"
	RoleFarmer             Role = "farmer"
)

// AllRoles is the closed set. A role outside it is a data error, not an
// unknown-but-harmless value.
var AllRoles = []Role{
	RoleSystemAdmin, RoleStateOfficer, RoleDistrictMagistrate, RoleTehsildar,
	RoleMandalOfficer, RoleTalathi, RoleKrushiSahayak, RoleFarmer,
}

// Valid reports whether r is a known role.
func (r Role) Valid() bool { return slices.Contains(AllRoles, r) }

// Permission is a single verb on a single resource.
type Permission string

const (
	// Assessment lifecycle.
	PermAssessmentCreate   Permission = "assessment:create"
	PermAssessmentRead     Permission = "assessment:read"
	PermAssessmentReview   Permission = "assessment:review"
	PermAssessmentApprove  Permission = "assessment:approve"
	PermAssessmentEscalate Permission = "assessment:escalate"

	// Survey events — the district's calamity notification.
	//
	// event:declare is separate from event:create because they are different
	// acts: drafting the notification is clerical, signing it is the
	// Collector committing the district to a survey.
	PermEventCreate  Permission = "event:create"
	PermEventRead    Permission = "event:read"
	PermEventDeclare Permission = "event:declare"
	PermEventClose   Permission = "event:close"

	// The Crop Loss Assessment Report -- the form a farmer files and an
	// officer acknowledges.
	//
	// report:review covers acknowledging and moving a filed report onward. It
	// is separate from report:create because filing and receiving are two
	// sides of the same counter: the farmer files, the administration
	// acknowledges, and one person doing both unsupervised is the thing the
	// acknowledgement slip exists to prevent.
	PermReportCreate Permission = "report:create"
	PermReportRead   Permission = "report:read"
	PermReportReview Permission = "report:review"

	// report:verify is the village authority recording what they found in the
	// field. report:adjudicate is setting the figure that leaves the system.
	//
	// Separate, and held by different roles, because the officers who verify
	// must not be the ones who decide on their own verification -- routing it
	// upward is the entire point of the pair.
	PermReportVerify     Permission = "report:verify"
	PermReportAdjudicate Permission = "report:adjudicate"

	// Self-survey: the farmer's own photographs of the damage.
	//
	// survey:capture is held by the farmer as well as by field officers,
	// which is the point -- a village pair cannot walk every parcel in a
	// taluka after a hailstorm, and the farmer is already standing in the
	// field. What makes that usable is the gates on the capture, not a
	// restriction on who may take one.
	PermSurveyCapture Permission = "survey:capture"

	PermSurveyCreate Permission = "survey:create"
	PermSurveyRead   Permission = "survey:read"

	// farmer:enrol creates a farmer's account from the registry record.
	//
	// Held by the officers who sit across a counter from the farmer, not by
	// the system administrator: enrolment is a registry lookup and an
	// identity check done in person, and the administrator has neither the
	// farmer in front of them nor the registry access to do it.
	PermFarmerEnrol Permission = "farmer:enrol"

	// Registry reads.
	PermFarmerRead    Permission = "farmer:read"
	PermParcelRead    Permission = "parcel:read"
	PermReferenceRead Permission = "reference:read"

	// Administration.
	PermUserCreate Permission = "user:create"
	PermUserRead   Permission = "user:read"
	PermUserUpdate Permission = "user:update"
	PermRoleGrant  Permission = "role:grant"
	PermAuditRead  Permission = "audit:read"

	// PermPlatformRead reads how the platform itself is running: which
	// integrations are configured, what the queues are holding, whether the
	// evidence store and the analytics engine are answering.
	//
	// Held by nobody who uses the platform, only by whoever runs it. The
	// distinction is real and was got wrong once already -- the data-source
	// page was gated on reference:read, which nearly every officer holds, so
	// a Talathi in a village was being shown Earth Engine's health.
	PermPlatformRead Permission = "platform:read"

	// The farmer's own side of the transaction.
	PermClaimSubmit Permission = "claim:submit"

	// Grievances. A farmer files and withdraws their own; an officer reads
	// the ones in their villages and answers the ones put in front of them.
	//
	// grievance:decide is deliberately not held by the Talathi or Krushi
	// Sahayak. They are the officers whose verification is usually what is
	// being appealed, and an appeal answered by the person appealed against
	// is not an appeal. The store refuses the exact case too, but a
	// permission that cannot be held is better than a refusal at the end of
	// a form somebody has already filled in.
	PermGrievanceFile   Permission = "grievance:file"
	PermGrievanceRead   Permission = "grievance:read"
	PermGrievanceAssign Permission = "grievance:assign"
	PermGrievanceDecide Permission = "grievance:decide"

	// Retention is the one irreversible operation in the system, so it sits
	// with the administrator rather than with anyone holding a district.
	// Reading what the policy says is separate from running it.
	PermRetentionRead Permission = "retention:read"
	PermRetentionRun  Permission = "retention:run"
)

// matrix is the whole of the policy.
//
// Two decisions in it are load-bearing and easy to undo by accident:
//
//   - RoleSystemAdmin can create accounts and read the audit log, and can
//     touch nothing about a claim. That half still holds: the administrator
//     is the account-of-last-resort and has no authority over a payout.
//
//     The converse no longer holds. A District Magistrate and a Tehsildar can
//     now both create accounts and adjudicate reports, which means one officer
//     can in principle staff a village with an account they control and carry
//     a claim the whole way up. That was a deliberate trade -- a district
//     cannot wait on an administrator to add a Talathi mid-calamity -- and
//     the audit trail is what stands in its place. delegation.go carries the
//     full reasoning and the two limits that remain: rank and place.
//
//   - Talathi and Krushi Sahayak both carry assessment:review. The joint
//     panchnama is signed by the revenue side and the agriculture side
//     together; a review model where either alone suffices is not the process
//     these officers actually follow.
var matrix = map[Role][]Permission{
	RoleSystemAdmin: {
		PermUserCreate, PermUserRead, PermUserUpdate, PermRoleGrant,
		PermAuditRead, PermReferenceRead,
		PermRetentionRead, PermRetentionRun,
		PermPlatformRead,
	},

	RoleStateOfficer: {
		PermAssessmentRead, PermAssessmentEscalate,
		PermGrievanceRead, PermRetentionRead,
		// Oversight, not declaration. The user's process is that a district
		// issues the notification; a calamity crossing districts is two
		// events, because two Collectors sign. Adding PermEventCreate here is
		// the one-line change if that is ever wanted.
		PermEventRead, PermReportRead,
		PermSurveyRead, PermFarmerRead, PermParcelRead, PermReferenceRead,
		PermUserRead, PermAuditRead,
		// Staffing the districts: a state officer appoints each district's
		// authority, who then staffs the district beneath them. Limited to
		// District Magistrates posted inside the state by delegation.go.
		PermUserCreate, PermUserUpdate, PermRoleGrant,
	},

	// The District Magistrate is the only role that declares a calamity, and
	// no longer decides claims.
	//
	// PermReportAdjudicate was taken away on 2026-09-28, when the village pair
	// became the deciding rung and the district stopped being one. What is
	// left is what a Collector actually does here: declare the calamity, staff
	// the district, read and aggregate what its officers decided, and hear
	// appeals -- which is a stronger position for hearing them from than
	// having signed the decision being appealed.
	RoleDistrictMagistrate: {
		PermAssessmentRead, PermAssessmentReview, PermAssessmentApprove,
		PermAssessmentEscalate,
		PermGrievanceRead, PermGrievanceAssign, PermGrievanceDecide,
		PermEventCreate, PermEventRead, PermEventDeclare, PermEventClose,
		PermReportRead, PermReportReview,
		PermSurveyRead, PermFarmerRead, PermParcelRead, PermReferenceRead,
		// Staffing the district. See delegation.go for what was given up to
		// put these here and what still bounds them: a Collector may create,
		// post and suspend the roles beneath their own, inside their own
		// district, and nothing else.
		PermUserCreate, PermUserRead, PermUserUpdate, PermRoleGrant,
		PermAuditRead,
	},

	RoleTehsildar: {
		PermAssessmentRead, PermAssessmentReview, PermAssessmentApprove,
		PermAssessmentEscalate, PermEventRead,
		PermGrievanceRead, PermGrievanceAssign, PermGrievanceDecide,
		PermReportRead, PermReportReview, PermReportAdjudicate,
		PermSurveyRead, PermFarmerRead, PermParcelRead, PermReferenceRead,
		// Staffing the tehsil: Circle Officers, Talathis and Krushi Sahayaks
		// posted inside it. Not a Tehsildar -- that would be promotion, not
		// delegation. See delegation.go.
		PermUserCreate, PermUserRead, PermUserUpdate, PermRoleGrant,
	},

	RoleMandalOfficer: {
		PermAssessmentCreate, PermAssessmentRead, PermAssessmentReview,
		PermAssessmentEscalate, PermEventRead,
		// Reads them, and may file one at a counter for a farmer who cannot.
		// Does not decide: a Circle Officer's own arbitration is often what
		// is being appealed.
		PermGrievanceRead, PermGrievanceFile,
		PermReportCreate, PermReportRead, PermReportReview,
		PermReportVerify, PermReportAdjudicate,
		PermSurveyCapture, PermSurveyRead,
		PermFarmerEnrol, PermFarmerRead, PermParcelRead, PermReferenceRead,
	},

	RoleTalathi: {
		PermAssessmentCreate, PermAssessmentRead, PermAssessmentReview,
		PermGrievanceRead, PermGrievanceFile,
		PermEventRead, PermReportCreate, PermReportRead, PermReportReview,
		PermReportVerify,
		PermSurveyCreate, PermSurveyCapture, PermSurveyRead,
		PermFarmerEnrol, PermFarmerRead, PermParcelRead, PermReferenceRead,
	},

	RoleKrushiSahayak: {
		PermAssessmentCreate, PermAssessmentRead, PermAssessmentReview,
		PermGrievanceRead, PermGrievanceFile,
		PermEventRead, PermReportCreate, PermReportRead, PermReportReview,
		PermReportVerify,
		PermSurveyCreate, PermSurveyCapture, PermSurveyRead,
		PermFarmerEnrol, PermFarmerRead, PermParcelRead, PermReferenceRead,
	},

	// A farmer must see the event they are filing against -- its window and
	// its deadline are the terms they are being held to.
	RoleFarmer: {
		// No PermAssessmentRead. An assessment carries the computer's loss
		// figure, and a farmer is shown a figure only once both village
		// officers have recorded; reading assessments directly went around
		// that. The decided claim's copy is reached through the report, gated
		// on the decision: GET /reports/{id}/assessment-report.
		PermClaimSubmit, PermEventRead,
		// The right this whole table exists for.
		PermGrievanceFile, PermGrievanceRead,
		PermReportCreate, PermReportRead,
		// The farmer photographs their own damage. This is the self-survey.
		PermSurveyCapture, PermSurveyRead,
		PermFarmerRead, PermParcelRead,
	},
}

// Permissions returns what a role carries. The returned slice is a copy.
func Permissions(r Role) []Permission {
	return slices.Clone(matrix[r])
}

// RoleCan reports whether a role carries a permission on its own.
func RoleCan(r Role, p Permission) bool {
	return slices.Contains(matrix[r], p)
}

// AssignableRoles is what a system administrator may grant.
//
// RoleFarmer is absent: a farmer account is created by registry enrolment
// against AgriStack, not by an administrator deciding someone is a farmer.
var AssignableRoles = []Role{
	RoleSystemAdmin, RoleStateOfficer, RoleDistrictMagistrate, RoleTehsildar,
	RoleMandalOfficer, RoleTalathi, RoleKrushiSahayak,
}

// Assignable reports whether a role may be granted through the admin API.
func Assignable(r Role) bool { return slices.Contains(AssignableRoles, r) }

// RequiredLevel is the jurisdiction level a role is normally posted at.
//
// Advisory rather than enforced: acting charge at a level above one's own is
// routine, and a system that refuses to record it gets worked around with a
// shared login. It is used to warn on a grant that looks like a mistake.
var RequiredLevel = map[Role]Level{
	RoleStateOfficer:       LevelState,
	RoleDistrictMagistrate: LevelDistrict,
	RoleTehsildar:          LevelSubDistrict,
	RoleMandalOfficer:      LevelCircle,
	RoleTalathi:            LevelVillage,
	RoleKrushiSahayak:      LevelVillage,
}
