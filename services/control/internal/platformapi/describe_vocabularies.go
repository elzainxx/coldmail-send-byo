package platformapi

// describe_vocabularies.go — fix-plan items 6 and 8: the two `describe` additions that let a client
// stop hardcoding this module's vocabularies, plus the declared value set for `pricing.model`.
//
// THE DEFECT (audit UI questions 1–4). `describe` told a caller which routes exist and what shape
// their bodies have, but never which VALUES those bodies can carry. So every UI hardcoded three
// separate vocabularies out of prose documentation — the per-module special-casing the whole
// alignment effort exists to remove — and, worse, a module ADDING an enum value broke those clients
// silently, which is the exact failure additive-only versioning (Rule 5) was supposed to prevent.
// A value set that is published is a value set a client can branch on safely; one that is only
// documented is a value set that drifts the first time someone runs ALTER TYPE ... ADD VALUE.
//
// EVERYTHING HERE IS DERIVED. That is the whole design, and it is the same rule already applied to
// `cost_fields_inert` (computed from `unit`, never a literal) and to contract_drift.sh's route
// counts (`${#ARRAY[@]}`, never a number). Specifically:
//
//   - the DB-backed vocabularies read pg_enum LIVE, per request. An operator who adds an enum value
//     changes what describe publishes with no code edit and no redeploy. A hardcoded list would be
//     correct the day it was written and a lie afterwards — and it would be a lie precisely in the
//     situation the field exists to cover.
//   - error_codes[] is built by walking codeStatus, the SAME map writeError resolves a status from.
//     It cannot list a code the module cannot emit, and it cannot omit one the module can.
//   - the non-DB vocabularies read the same Go constants the emitting code paths use, so a renamed
//     constant moves the declaration with it rather than leaving it stale.
//
// WHY THIS FILE FAILS THE REQUEST ON A DB ERROR, where readUnitPriceCredits deliberately does the
// opposite. readUnitPriceCredits falls back to a seeded default because a tunable integer has an
// honest default and a transient blip must not 500 every quote in flight. A vocabulary has NO honest
// default: the only fallback available is a hardcoded list, which is exactly the stale-declaration
// failure this item exists to remove — a client told "these are the values" would be misinformed
// rather than merely unserved. describe is a discovery call, not the send path; an error a client
// retries is strictly better than a confident wrong answer. The contrast is deliberate, and both
// directions are the same principle applied to two different kinds of value.

import (
	"context"
	"sort"
)

// --- item 6: pricing.model's declared value set ---

// The pricing models the platform contract defines (fix-plan item 6, canonical contract §1.7).
// ADDITIVE-ONLY (Rule 5): a value is never renamed or removed, only appended. `free` is declared
// though NEITHER sender reports it — deliberately, because the entire point of publishing the set
// is that a client can write its branch ONCE and handle a module it has not met yet. Declaring only
// the two values that exist today would leave the third module's launch as a client-side code
// change, which is the defect item 6 names.
const (
	pricingModelFlatPerRecipient = "flat_per_recipient" // module 2: charged per recipient
	pricingModelFlatSubscription = "flat_subscription"  // module 3: flat monthly, no per-send charge
	pricingModelFree             = "free"               // declared for forward compatibility; unused today
)

// pricingModels is the declared set, in contract order. handleDescribe publishes it AND reports this
// module's own model; describe_vocabularies_test.go asserts the reported model is a MEMBER of it, so
// a module cannot report a model it did not declare.
var pricingModels = []string{
	pricingModelFlatPerRecipient,
	pricingModelFlatSubscription,
	pricingModelFree,
}

// --- item 8: error_codes[] ---

// describeErrorCodes renders every error code this module can emit, with the HTTP status it maps to.
//
// DERIVED FROM codeStatus — the same map writeError consults to pick a status. Hand-listing here
// would produce the one failure mode a published code list must not have: a caller building an
// exhaustive switch off a list that omits a code the module actually returns. Because both the list
// and the wire behaviour read one map, they cannot disagree.
//
// The status is included because it is what makes the list ACTIONABLE for the unknown-code rule
// (fix-plan item 7): a client that meets a code it does not recognise degrades by status. M2 emits
// five codes and M3 seven at the same /v1; a client that can read both lists needs no per-module
// knowledge to do the right thing with either.
//
// The fixed message is NOT published. It is already in every error envelope this module sends, it is
// operator-facing English rather than a branchable value, and publishing it would invite a client to
// match on message text — the coupling the coded envelope exists to eliminate.
func describeErrorCodes() []map[string]any {
	out := make([]map[string]any, 0, len(codeStatus))
	for code, status := range codeStatus {
		out = append(out, map[string]any{"code": code, "http_status": status})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i]["http_status"].(int) < out[j]["http_status"].(int) ||
			(out[i]["http_status"].(int) == out[j]["http_status"].(int) &&
				out[i]["code"].(string) < out[j]["code"].(string))
	})
	return out
}

// --- item 8: vocabularies{} ---

// exposedDBEnum is one send_byo enum type whose values can appear in a Domain-A response body.
//
// `Fields` lists the response paths that carry the value. It is documentation, not the source of
// truth for the values — but it is not unchecked either: describe_vocabularies_test.go asserts every
// leaf field name is a real JSON tag somewhere in this package's production files, so a renamed or
// deleted response field cannot leave a dangling path here.
type exposedDBEnum struct {
	Type   string
	Fields []string
}

// exposedDBEnums names the enum types, NOT their values — the values come from pg_enum at request
// time. This list is necessarily hand-maintained, because "which enums can cross the Domain-A
// boundary" is a statement about the response contract that no query can answer. What keeps it
// honest is the pair of controls in describe_vocabularies_test.go: every entry's values must match
// the live type (so a renamed type fails), and every send_byo enum type must appear either here or
// in notExposedDBEnums (so a NEW enum cannot be quietly ignored, and a newly-exposed one cannot stay
// undeclared).
var exposedDBEnums = []exposedDBEnum{
	{Type: "credential_verification_state", Fields: []string{"account_object.verification.state"}},
	{Type: "dmarc_policy", Fields: []string{"account_object.from_alignment.dmarc_policy"}},
	{Type: "error_class", Fields: []string{"report.error_class", "report.items[].error_class"}},
	{Type: "event_actor", Fields: []string{"account_events_response.items[].actor"}},
	{Type: "from_verification_state", Fields: []string{"account_object.from_alignment.state"}},
	{Type: "job_state", Fields: []string{"report.state", "report.items[].state"}},
	{Type: "notification_severity", Fields: []string{"notifications_list_response.items[].severity"}},
	{Type: "notification_state", Fields: []string{"notifications_list_response.items[].state"}},
	{Type: "pause_reason", Fields: []string{"account_object.status.pause_reason"}},
	{Type: "quarantine_reason", Fields: []string{"account_object.status.quarantine_reason"}},
	{Type: "routing_strategy", Fields: []string{"settings_object.routing_strategy"}},
}

// notExposedDBEnums is the DELIBERATE other half of the accounting: a send_byo enum type that this
// surface never puts in a response, with the reason. It exists so the coverage control can demand
// that EVERY enum type in the schema is accounted for one way or the other. Without it, "not in
// exposedDBEnums" would be indistinguishable from "nobody has looked", and a genuinely new
// user-facing enum could ship undeclared — the silent-drift failure this item is about, reappearing
// one level up in the mechanism meant to prevent it.
var notExposedDBEnums = map[string]string{
	"box_status": "worker-fleet state. Operator-only (/v1/admin/health); no Domain-A response " +
		"carries it — a platform tenant has no worker to reason about.",
	"suspend_reason": "user_suspensions.reason. platformapi reads only the BOOLEAN suspended flag " +
		"(store.go's enqueue gate) and reports the outcome as the `conflict` error code; the reason " +
		"is operator context, never returned to the tenant.",
}

// staticVocabulary is a value set whose source of truth is Go constants or a CHECK constraint rather
// than a pg_enum type — the vocabularies a client must branch on that are not backed by a DB enum.
// Leaving these out would have made `vocabularies{}` look complete while omitting exactly the codes
// a client is most likely to switch on (quote's reasons, details[].issue).
type staticVocabulary struct {
	Name   string
	Values []string
	Fields []string
}

// staticVocabularies — every entry's Values reference the SAME constants the emitting code path
// uses, never a re-typed literal, so the declaration travels with the value.
var staticVocabularies = []staticVocabulary{
	{
		Name:   "detail_issue",
		Values: []string{issueRequired, issueMalformed, issueOutOfRange, issueNotFound, issueConflict},
		Fields: []string{"error.details[].issue"},
	},
	{
		Name: "quote_reason_code",
		Values: []string{
			quoteReasonDispatchDisabled, quoteReasonExceedsQueueCap, quoteReasonNoEligibleAccounts,
			quoteReasonAllAccountsUnverified, quoteReasonFromGateBlocked,
		},
		Fields: []string{"quote.reasons[].code"},
	},
	{
		Name:   "quote_warning_code",
		Values: []string{quoteWarningLongDuration},
		Fields: []string{"quote.warnings[].code"},
	},
	{
		Name:   "quote_estimate_basis",
		Values: []string{quoteEstimateBasisCurrentCapacity},
		Fields: []string{"quote.capacity.estimate_basis"},
	},
	{
		// The values of a TEXT column constrained by notifications_subject_type_check (send_byo_0033),
		// not a pg_enum type — so the control for this one reads pg_get_constraintdef and asserts each
		// declared value appears in it. 'pool' is declared because the CHECK allows it, even though no
		// write site emits it today (0033's own note keeps it for Rule-5 stability): a client that
		// hard-fails on an unexpected value must not break the day one does.
		Name:   "notification_subject_type",
		Values: []string{"account", "campaign", "pool"},
		Fields: []string{"notifications_list_response.items[].subject_type"},
	},
	{
		// notifications.condition is also TEXT with no CHECK — its vocabulary is defined purely by the
		// write sites. Both are already pinned by notifications_read_test.go's write-site test (which
		// fails on a third INSERT), so that test is this entry's control: a new condition cannot ship
		// without a review that sees this list.
		Name:   "notification_condition",
		Values: []string{"account_quarantined", "campaign_expired"},
		Fields: []string{"notifications_list_response.items[].condition"},
	},
	{
		Name:   "bulk_add_row_status",
		Values: []string{bulkAddStatusCreated, bulkAddStatusRejected},
		Fields: []string{"accounts_bulk_add_response.results[].status"},
	},
	{
		Name:   "pricing_model",
		Values: pricingModels,
		Fields: []string{"describe.pricing.model"},
	},
}

// enumValuesQuery reads the live labels of every send_byo enum type in one round trip, in the type's
// own declared sort order (enumsortorder, not alphabetical — the order the schema author chose is
// meaningful for states like unverified→verified→failed, and a client rendering a legend should see
// it). send_byo_app can read pg_enum: it is a catalog table readable by PUBLIC, so this needs no
// grant change and no operator role.
const enumValuesQuery = `SELECT t.typname, e.enumlabel
	FROM pg_type t
	JOIN pg_enum e ON e.enumtypid = t.oid
	JOIN pg_namespace n ON n.oid = t.typnamespace
	WHERE n.nspname = 'send_byo'
	ORDER BY t.typname, e.enumsortorder`

// describeVocabularies builds the published vocabularies object. Returns an apiError rather than a
// partial answer on any failure — see this file's header on why describe fails closed here while
// readUnitPriceCredits falls back.
func (s *Server) describeVocabularies(ctx context.Context) (map[string]any, *apiError) {
	live, aerr := s.liveEnumValues(ctx)
	if aerr != nil {
		return nil, aerr
	}
	out := make(map[string]any, len(exposedDBEnums)+len(staticVocabularies))
	for _, de := range exposedDBEnums {
		vals, ok := live[de.Type]
		if !ok || len(vals) == 0 {
			// A declared type that the schema does not have is a code/schema mismatch, not a blip:
			// publishing the remaining vocabularies while silently dropping this one would hand a
			// client a set it believes is complete. Named in the internal reason only (two-vocabulary
			// rule) — the caller gets the fixed internal_error sentence.
			return nil, newErr(codeInternalError, "describe_vocabulary_missing_enum_type: "+de.Type)
		}
		out[de.Type] = map[string]any{"source": "db_enum", "values": vals, "fields": de.Fields}
	}
	for _, sv := range staticVocabularies {
		out[sv.Name] = map[string]any{"source": "module_constant", "values": sv.Values, "fields": sv.Fields}
	}
	return out, nil
}

// liveEnumValues returns every send_byo enum type's labels, keyed by type name.
func (s *Server) liveEnumValues(ctx context.Context) (map[string][]string, *apiError) {
	rows, err := s.db.Query(ctx, enumValuesQuery)
	if err != nil {
		return nil, newErr(codeInternalError, "describe_enum_query_error: "+err.Error())
	}
	defer rows.Close()
	live := map[string][]string{}
	for rows.Next() {
		var typname, label string
		if err := rows.Scan(&typname, &label); err != nil {
			return nil, newErr(codeInternalError, "describe_enum_scan_error: "+err.Error())
		}
		live[typname] = append(live[typname], label)
	}
	if err := rows.Err(); err != nil {
		return nil, newErr(codeInternalError, "describe_enum_rows_error: "+err.Error())
	}
	return live, nil
}
