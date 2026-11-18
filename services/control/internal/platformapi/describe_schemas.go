package platformapi

// describe_schemas.go — the `schemas` object describe's request_schema_ref/response_schema_ref index
// into (describe_routes.go). Split out of describe_routes.go by the Phase 3 close-out (C6) when the
// fragments were corrected to say what the handlers actually return.
//
// WHAT WAS WRONG (FW-M3-16, plus two more of the same class). `accounts_list_response` and
// `account_events_response` declared a top-level key `results` where both handlers return `items`, and
// `account_object` was declared FLAT (`disabled`, `paused`, `quarantined` at the top) where the real
// object nests `status{}`, `verification{}` and `from_alignment{}`. A client that generates its parser
// from describe — the whole point of describe — read the wrong key and saw an empty list, not an error.
// The test that drove every declared route asserted the STATUS only, so both halves stayed internally
// consistent and nothing compared one with the other.
//
// WHAT HOLDS NOW. Every fragment is written from the handler's own response type, and
// describe_schemas_test.go compares each fragment with the bodies the REAL handlers produce, in BOTH
// directions: a key a handler returns that the fragment does not declare is red, a key the fragment
// declares that no handler response carried is red (an optional key is allowed only through a named,
// reasoned exemption list that is itself checked), and a `required` key missing from a response is
// red. A paired control renames one key in either side and must be caught by the same checker.
//
// SHAPE. JSON-Schema-shaped, not exhaustive JSON Schema: `type` (a string or a list that may include
// "null"), `properties`, `required` (the keys that are ALWAYS present — a key omitted until set is
// declared but not required), `items`, `enum`, and `$ref` into this object. Closed value sets that
// the module publishes live (job_state, error_class, ...) are NOT repeated here: they are in
// describe's `vocabularies{}`, which cannot go stale because it is read from pg_enum per request.

// schObj builds an object fragment. required lists the keys a response ALWAYS carries.
func schObj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

// schNullableObj is schObj for an object that may also be null.
func schNullableObj(props map[string]any, required ...string) map[string]any {
	m := schObj(props, required...)
	m["type"] = []string{"object", "null"}
	return m
}

func schArr(items any) map[string]any { return map[string]any{"type": "array", "items": items} }

func schRef(name string) map[string]any { return map[string]any{"$ref": "#/schemas/" + name} }

// describeSchemas is the JSON-Schema-shaped `schemas` object §5.1's request_schema_ref/
// response_schema_ref index into. Deliberately not exhaustive JSON-Schema (no format validators) —
// enough for a caller to construct a valid request body and know exactly what shape to expect back,
// which is what "sufficient for discovery" (§5.1) requires.
func describeSchemas() map[string]any {
	str := map[string]any{"type": "string"}
	strOrNull := map[string]any{"type": []string{"string", "null"}}
	uuid := map[string]any{"type": "string", "format": "uuid"}
	boolT := map[string]any{"type": "boolean"}
	intT := map[string]any{"type": "integer"}

	// fieldDetail is the {field, issue} pair every details[] carries (errors.go).
	fieldDetailSchema := schObj(map[string]any{"field": str, "issue": str}, "field", "issue")

	return map[string]any{
		"accounts_create_request": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"user_id": uuid, "smtp_host": str, "smtp_port": intT, "smtp_username": str,
				"smtp_secret": str, "from_address": str, "from_name": str, "daily_limit": intT,
				"acknowledge_alignment_warning": boolT,
			},
			"required": []string{"user_id", "smtp_host", "smtp_username", "smtp_secret", "from_address"},
		},
		"accounts_update_request": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"smtp_host": str, "smtp_port": intT, "smtp_username": str, "smtp_secret": str,
				"from_address": str, "from_name": str, "daily_limit": intT,
				"acknowledge_alignment_warning": boolT,
			},
		},
		"accounts_bulk_add_request": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"user_id": uuid,
				"accounts": map[string]any{"type": "array", "items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"smtp_host": str, "smtp_port": intT, "smtp_username": str, "smtp_secret": str,
						"from_address": str, "from_name": str, "daily_limit": intT,
						"acknowledge_alignment_warning": boolT,
					},
				}},
			},
			"required": []string{"user_id", "accounts"},
		},
		// accounts.bulk_add's body. The key is `results` HERE and it is right: it is the one list on
		// this surface whose handler says `results` (every other list says `items`). `row` is the
		// 1-based input index; a `created` row carries the verification and alignment facts, a
		// `rejected` row carries `code` (and `details` for a validation failure). `verification` is a
		// STRING on this route and an object on the account — kept as built, never renamed.
		"accounts_bulk_add_response": schObj(map[string]any{
			"request_id": str,
			"results": schArr(schObj(map[string]any{
				"row":                    intT,
				"status":                 map[string]any{"type": "string", "enum": []string{"created", "rejected"}},
				"account_id":             uuid,
				"verification":           str,
				"code":                   str,
				"smtp_response":          str,
				"from_alignment_state":   str,
				"from_alignment_message": str,
				"details":                schArr(fieldDetailSchema),
			}, "row", "status")),
		}, "request_id", "results"),
		"accounts_test_request": map[string]any{
			"type":       "object",
			"properties": map[string]any{"acknowledge_alignment_warning": boolT},
		},
		// accounts.test's body: two independent verdicts. `code` on verification is present when the
		// credential check did not verify (a verify_* code) or could not run (verify_error /
		// verification_unavailable); on from_alignment it is present when the From check could not run
		// (from_dns_unavailable) or the check's result could not be saved; `error` is present only when
		// the From check ran but its result could not be saved.
		"accounts_test_response": schObj(map[string]any{
			"verification": schObj(map[string]any{
				"state":         str,
				"smtp_response": str,
				"code":          str,
			}, "state"),
			"from_alignment": schObj(map[string]any{
				"state":   str,
				"code":    str,
				"message": str,
				"error":   str,
			}, "state"),
		}, "verification", "from_alignment"),
		"test_send_response": schObj(map[string]any{"job_id": str, "state": str}, "job_id", "state"),
		// account_object is the account as every account route returns it (create 201, get, update,
		// the lifecycle verbs, unquarantine). It is NESTED — status{}, verification{}, from_alignment{} —
		// exactly as accountResource serialises. A key omitted until set (quarantine_reason, verified_at,
		// code, smtp_response, the alignment evidence) is declared but not required.
		"account_object": schObj(map[string]any{
			"id": uuid, "user_id": uuid, "smtp_host": str, "smtp_port": intT, "smtp_username": str,
			"from_address": str, "from_name": strOrNull, "has_credential": boolT,
			"status": schObj(map[string]any{
				"paused":            boolT,
				"pause_reason":      strOrNull,
				"quarantined":       boolT,
				"quarantine_reason": str,
				"quarantined_at":    str,
				"disabled":          boolT,
			}, "paused", "pause_reason", "quarantined", "disabled"),
			"verification": schObj(map[string]any{
				"state":         str,
				"verified_at":   str,
				"verify_at":     str,
				"code":          str,
				"smtp_response": str,
			}, "state"),
			"from_alignment": schObj(map[string]any{
				"state":              str,
				"dmarc_policy":       str,
				"checked_at":         str,
				"last_attempt_at":    str,
				"last_attempt_error": str,
				"message":            str,
				"acknowledged_at":    str,
			}, "state"),
			"daily_limit": intT, "created_at": str, "updated_at": str,
		}, "id", "user_id", "smtp_host", "smtp_port", "smtp_username", "from_address", "from_name",
			"has_credential", "status", "verification", "from_alignment", "daily_limit", "created_at", "updated_at"),
		// accounts.list's body. The list key is `items` (the handler's), never `results`.
		"accounts_list_response": schObj(map[string]any{
			"items":       schArr(schRef("account_object")),
			"next_cursor": strOrNull,
		}, "items", "next_cursor"),
		// accounts.events' body: the audit trail. from_flags/to_flags are null on the row that has no
		// "before" (the creation row).
		"account_events_response": schObj(map[string]any{
			"items": schArr(schObj(map[string]any{
				"id": uuid, "account_id": map[string]any{"type": []string{"string", "null"}, "format": "uuid"},
				"user_id": uuid, "actor": str, "action": str,
				"from_flags": schNullableObj(map[string]any{
					"paused": boolT, "pause_reason": strOrNull, "quarantined": boolT, "disabled": boolT,
				}, "paused", "pause_reason", "quarantined", "disabled"),
				"to_flags": schNullableObj(map[string]any{
					"paused": boolT, "pause_reason": strOrNull, "quarantined": boolT, "disabled": boolT,
				}, "paused", "pause_reason", "quarantined", "disabled"),
				"reason": strOrNull, "request_id": strOrNull, "created_at": str,
			}, "id", "account_id", "user_id", "actor", "action", "from_flags", "to_flags", "reason", "request_id", "created_at")),
			"next_cursor": strOrNull,
		}, "items", "next_cursor"),
		// notifications.list (audit D5 / fix-plan item 9). The property is "items" — the key the
		// handler ACTUALLY returns. state's enumerated values are the send_byo.notification_state type
		// as it actually stands after send_byo_0047, INCLUDING 'cleared' — which dispatch never writes
		// (ClearNotification stamps cleared_at and leaves state alone). It is declared because the enum
		// declares it and a client that hard-fails on an unexpected value must not break if a later
		// change starts writing it; cleared_at, not state, is the field to read for "is this resolved".
		"notifications_list_response": schObj(map[string]any{
			"items": schArr(schObj(map[string]any{
				"id": uuid, "user_id": uuid, "condition": str,
				"subject_type": map[string]any{"type": []string{"string", "null"},
					"enum": []any{"account", "campaign", "pool", nil}},
				"subject_id": map[string]any{"type": []string{"string", "null"}, "format": "uuid"},
				"severity":   map[string]any{"type": "string", "enum": []string{"critical", "warning", "info"}},
				"state": map[string]any{"type": "string",
					"enum": []string{"pending", "delivered", "suppressed", "cleared"}},
				"payload":       map[string]any{"type": "object"},
				"first_seen_at": str, "last_seen_at": str,
				"notified_at": strOrNull,
				"cleared_at":  strOrNull,
			}, "id", "user_id", "condition", "subject_type", "subject_id", "severity", "state", "payload",
				"first_seen_at", "last_seen_at", "notified_at", "cleared_at")),
			"next_cursor": strOrNull,
		}, "items", "next_cursor"),
		// settings.get / settings.put's RESPONSE. The request is settings_put_request (tenant is the
		// query, never the body).
		"settings_object": schObj(map[string]any{"user_id": uuid,
			"routing_strategy": map[string]any{"type": "string"}}, "user_id", "routing_strategy"),
		"settings_put_request": schObj(map[string]any{"routing_strategy": map[string]any{"type": "string"}},
			"routing_strategy"),
		"usage_response": schObj(map[string]any{
			"user_id": uuid, "from": str, "to": str,
			"accounts_configured": intT, "accounts_eligible": intT,
			"days": schArr(schObj(map[string]any{
				"date": str, "messages_attempted": intT, "messages_sent": intT, "messages_bounced": intT,
				"messages_failed": intT, "campaigns_run": intT, "peak_concurrency_used": intT,
				"distinct_destination_domains": intT,
			}, "date", "messages_attempted", "messages_sent", "messages_bounced", "messages_failed",
				"campaigns_run", "peak_concurrency_used", "distinct_destination_domains")),
		}, "user_id", "from", "to", "accounts_configured", "accounts_eligible", "days"),
	}
}
