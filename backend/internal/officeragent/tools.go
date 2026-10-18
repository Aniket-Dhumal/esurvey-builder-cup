package officeragent

import (
	"context"
	"fmt"
	"strings"

	"github.com/agribid/esurvey-platform/backend/internal/platform/page"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
	"github.com/agribid/esurvey-platform/backend/internal/report"
)

// Reports is the part of the report store the agent needs.
//
// An interface rather than the store itself, so the tools can be tested against a
// stand-in without a database -- and so that what the agent is able to reach is
// legible in one place. Every method takes the officer's scope, which is the whole
// point: there is no unscoped read here to accidentally call.
type Reports interface {
	List(ctx context.Context, f report.Filter) (page.Result[report.Summary], error)
	Get(ctx context.Context, id string, scope rbac.Scope) (*report.Report, error)
	ByReference(ctx context.Context, ref string, scope rbac.Scope) (*report.Report, error)
}

// ReadOnlyTools are the tools that only look.
//
// # Why the list is short
//
// Each tool is a thing the model can be wrong about. A narrow set of tools that
// answer clearly beats a wide set where the model picks badly -- and every one of
// these had to justify itself against "could the officer answer this faster by
// looking at their own screen".
func ReadOnlyTools(reports Reports) []Tool {
	return []Tool{
		{
			Name: "list_claims",
			Description: "Claims within this officer's posting, newest first. " +
				"Use for questions about how many, which villages, what states " +
				"they are in. Returns a summary per claim, not the full claim.",
			Parameters: map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"state": map[string]any{
						"type": "STRING",
						"description": "Narrow to one state: submitted, " +
							"assigned, verified, approved, rejected, returned. " +
							"Omit for all.",
					},
					"awaiting_level": map[string]any{
						"type": "STRING",
						"description": "Narrow to claims waiting at one rung of " +
							"the approval ladder, for questions about what is " +
							"pending on whom.",
					},
					"search": map[string]any{
						"type":        "STRING",
						"description": "Free text: a village name, a farmer name.",
					},
					"limit": map[string]any{
						"type":        "INTEGER",
						"description": "How many to return, at most 50.",
					},
				},
			},
			Run: func(
				ctx context.Context, scope rbac.Scope, args map[string]any,
			) (any, error) {
				limit := intArg(args, "limit", 20)
				if limit > 50 {
					// Capped rather than refused. The model asking for 500 is
					// asking the right question badly, and a page of 50 answers it
					// while keeping the prompt a sane size -- every claim in the
					// result is resent on every later turn.
					limit = 50
				}
				res, err := reports.List(ctx, report.Filter{
					Scope:         scope,
					State:         stringArg(args, "state"),
					AwaitingLevel: stringArg(args, "awaiting_level"),
					Page: page.Params{
						Limit: limit,
						Query: stringArg(args, "search"),
					},
				})
				if err != nil {
					return nil, fmt.Errorf("reading the claim list: %w", err)
				}
				return map[string]any{
					"total":   res.Total,
					"claims":  brief(res.Items),
					"showing": len(res.Items),
				}, nil
			},
		},
		{
			Name: "get_claim",
			Description: "One claim in full, by its reference (like " +
				"CLR-2026-496-00001) or its id. Use when asked why something was " +
				"decided, or what a claim contains.",
			Parameters: map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"reference": map[string]any{
						"type":        "STRING",
						"description": "The claim reference or id.",
					},
				},
				"required": []any{"reference"},
			},
			Run: func(
				ctx context.Context, scope rbac.Scope, args map[string]any,
			) (any, error) {
				ref := strings.TrimSpace(stringArg(args, "reference"))
				if ref == "" {
					return nil, fmt.Errorf("no claim reference was given")
				}

				// By reference first, because that is what an officer says out
				// loud and therefore what the model will have been given. An id
				// is the fallback, not the primary key of the conversation.
				rep, err := reports.ByReference(ctx, ref, scope)
				if err != nil {
					if rep, err = reports.Get(ctx, ref, scope); err != nil {
						// Not found and not-yours are the same answer here, and
						// deliberately: distinguishing them would tell an officer
						// that a claim exists outside their posting.
						return nil, fmt.Errorf(
							"no claim %q in your area", ref)
					}
				}
				return rep, nil
			},
		},
	}
}

// brief reduces a page of summaries to what a model needs.
//
// The full summary carries a dozen fields the model will not use, and every one of
// them is resent on every subsequent turn of the conversation. Trimming here is the
// difference between a chain of six turns that fits and one that does not.
func brief(items []report.Summary) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, s := range items {
		out = append(out, map[string]any{
			"reference":        s.Reference,
			"farmer_name":      s.FarmerName,
			"village":          s.VillageName,
			"sub_district":     s.SubDistrictName,
			"calamity":         s.CalamityType,
			"state":            s.State,
			"occurred_at":      s.OccurredAt.Format("2006-01-02"),
			"reported_at":      s.ReportedAt.Format("2006-01-02"),
			"filed_late":       s.FiledLate,
			"area_sown_ha":     s.AreaSownHa,
			"area_affected_ha": s.AreaAffectedHa,
			"damage_percent":   s.DamagePercent,
		})
	}
	return out
}

func stringArg(args map[string]any, key string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func intArg(args map[string]any, key string, fallback int) int {
	v, ok := args[key]
	if !ok {
		return fallback
	}
	switch n := v.(type) {
	case float64:
		// JSON numbers arrive as float64, which is how the model's arguments
		// always reach here.
		return int(n)
	case int:
		return n
	}
	return fallback
}
