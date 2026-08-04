package platformapi

// describe_routes.go — Phase 7 §5: the SINGLE route registry `routes()` (server.go) and
// `handleDescribe`'s `extension_routes` BOTH read, so describe is generated from the mux rather
// than hand-maintained (§5.1's own requirement — a hand-written list drifts silently the first
// time a route is added, which surfaces as a support ticket, not a test failure). Every extension
// route (everything beyond the core four: quote/run/report/describe) is declared here exactly
// once; server.go's routes() registers the mux from this same slice, so a route cannot exist on
// the mux without a description, and a description cannot exist without the mux entry it names
// (T6's own dispositive test — a client calling only what describe declares — is the thing that
// keeps the two from silently diverging).
//
// The four fields that make the declaration sufficient rather than decorative (§5.1): `auth` (which
// credential domain — Rule 5 forbids mixing Domain A/B), `scope` ("user" — operator routes are
// NEVER declared here, since describe is served on Domain A and enumerating the admin surface to a
// platform token is a free map of it), `id` (a stable identifier independent of the path), and
// request_schema_ref/response_schema_ref (indirection into `schemas` — a URL alone is not
// "discovery," Rule 3 forbids a shape check standing in for the behavioral requirement that a
// caller can actually construct a valid request from what's declared).

import "net/http"

// extensionRoute is one entry of §5.1's extension_routes[] — the full declaration a caller needs
// to discover AND call the route without reading source.
type extensionRoute struct {
	ID                string
	Method            string
	Path              string // Go 1.22 mux pattern, e.g. "/v1/send-byo/accounts/{id}"
	Auth              string // always "platform_service_token" on this Domain-A-only registry
	Scope             string // always "user" — operator routes are never declared here
	Description       string
	RequestSchemaRef  string // "" when the route takes no body (GET/DELETE-shaped)
	ResponseSchemaRef string
	Handler           http.HandlerFunc
}

// extensionRoutes is the ONE list server.go's routes() mounts and handleDescribe declares. Order
// is registration order (cosmetic — describe's own consumers must not rely on array order meaning
// anything, per the additive-only contract).
func (s *Server) extensionRoutes() []extensionRoute {
	return []extensionRoute{
		{ID: "accounts.create", Method: "POST", Path: "/v1/send-byo/accounts", Auth: "platform_service_token", Scope: "user",
			Description:       "Create an account for the caller's tenant. Verify-on-add is mandatory.",
			RequestSchemaRef:  "#/schemas/accounts_create_request",
			ResponseSchemaRef: "#/schemas/account_object",
			Handler:           s.handleAccountCreate},
		{ID: "accounts.bulk_add", Method: "POST", Path: "/v1/send-byo/accounts/bulk", Auth: "platform_service_token", Scope: "user",
			Description:       "Create N accounts in one request, paced verification.",
			RequestSchemaRef:  "#/schemas/accounts_bulk_add_request",
			ResponseSchemaRef: "#/schemas/accounts_bulk_add_response",
			Handler:           s.handleAccountBulkAdd},
		{ID: "accounts.list", Method: "GET", Path: "/v1/send-byo/accounts", Auth: "platform_service_token", Scope: "user",
			Description:       "List the calling user's own sender accounts.",
			ResponseSchemaRef: "#/schemas/accounts_list_response",
			Handler:           s.handleAccountList},
		{ID: "accounts.get", Method: "GET", Path: "/v1/send-byo/accounts/{id}", Auth: "platform_service_token", Scope: "user",
			Description:       "Read one account, scoped to the caller's own tenant.",
			ResponseSchemaRef: "#/schemas/account_object",
			Handler:           s.handleAccountGet},
		{ID: "accounts.update", Method: "PATCH", Path: "/v1/send-byo/accounts/{id}", Auth: "platform_service_token", Scope: "user",
			Description:       "Update an account; omitted fields are unchanged.",
			RequestSchemaRef:  "#/schemas/accounts_update_request",
			ResponseSchemaRef: "#/schemas/account_object",
			Handler:           s.handleAccountUpdate},
		{ID: "accounts.delete", Method: "DELETE", Path: "/v1/send-byo/accounts/{id}", Auth: "platform_service_token", Scope: "user",
			Description: "Soft-delete an account (retained through account_retention_window_sec).",
			Handler:     s.handleAccountDelete},
		{ID: "accounts.events", Method: "GET", Path: "/v1/send-byo/accounts/{id}/events", Auth: "platform_service_token", Scope: "user",
			Description:       "The account's own audit trail, scoped to the caller's tenant.",
			ResponseSchemaRef: "#/schemas/account_events_response",
			Handler:           s.handleAccountEvents},
		{ID: "accounts.disable", Method: "POST", Path: "/v1/send-byo/accounts/{id}/disable", Auth: "platform_service_token", Scope: "user",
			Description:       "Disable an account (manual, user-reversible).",
			ResponseSchemaRef: "#/schemas/account_object",
			Handler:           s.handleAccountDisable},
		{ID: "accounts.enable", Method: "POST", Path: "/v1/send-byo/accounts/{id}/enable", Auth: "platform_service_token", Scope: "user",
			Description:       "Re-enable a disabled account.",
			ResponseSchemaRef: "#/schemas/account_object",
			Handler:           s.handleAccountEnable},
		{ID: "accounts.pause", Method: "POST", Path: "/v1/send-byo/accounts/{id}/pause", Auth: "platform_service_token", Scope: "user",
			Description:       "Manually pause an account (does not affect the daily-limit/throttle auto-pauses).",
			ResponseSchemaRef: "#/schemas/account_object",
			Handler:           s.handleAccountPause},
		{ID: "accounts.unpause", Method: "POST", Path: "/v1/send-byo/accounts/{id}/unpause", Auth: "platform_service_token", Scope: "user",
			Description:       "Clear a MANUAL pause only (refuses to clear a non-manual reason).",
			ResponseSchemaRef: "#/schemas/account_object",
			Handler:           s.handleAccountUnpause},
		{ID: "accounts.unquarantine", Method: "POST", Path: "/v1/send-byo/accounts/{id}/unquarantine", Auth: "platform_service_token", Scope: "user",
			Description:       "Self-serve un-quarantine, rate-limited per day.",
			ResponseSchemaRef: "#/schemas/account_object",
			Handler:           s.handleAccountUnquarantine},
		{ID: "accounts.test", Method: "POST", Path: "/v1/send-byo/accounts/{id}/test", Auth: "platform_service_token", Scope: "user",
			Description:       "Connect + STARTTLS + AUTH only; re-runs the From-alignment check.",
			RequestSchemaRef:  "#/schemas/accounts_test_request",
			ResponseSchemaRef: "#/schemas/accounts_test_response",
			Handler:           s.handleAccountTest},
		{ID: "accounts.test_send", Method: "POST", Path: "/v1/send-byo/accounts/{id}/test-send", Auth: "platform_service_token", Scope: "user",
			Description:       "A real send to the account's own from_address, forcing this specific account.",
			ResponseSchemaRef: "#/schemas/test_send_response",
			Handler:           s.handleAccountTestSend},
		{ID: "settings.get", Method: "GET", Path: "/v1/send-byo/settings", Auth: "platform_service_token", Scope: "user",
			Description:       "Read the caller's own routing_strategy.",
			ResponseSchemaRef: "#/schemas/settings_object",
			Handler:           s.handleUserSettingsGet},
		{ID: "settings.put", Method: "PUT", Path: "/v1/send-byo/settings", Auth: "platform_service_token", Scope: "user",
			Description:       "Write the caller's own routing_strategy.",
			RequestSchemaRef:  "#/schemas/settings_object",
			ResponseSchemaRef: "#/schemas/settings_object",
			Handler:           s.handleUserSettingsPut},
		{ID: "usage.get", Method: "GET", Path: "/v1/send-byo/usage", Auth: "platform_service_token", Scope: "user",
			Description:       "Per-user, per-UTC-day usage metrics, derived from jobs/job_events (§6).",
			ResponseSchemaRef: "#/schemas/usage_response",
			Handler:           s.handleUsage},
	}
}

// describeExtensionRoutesJSON renders §5.1's extension_routes[] shape from the registry. The
// path is converted from the Go 1.22 mux's {id}-brace syntax to nothing else — the spec's own
// example uses the same brace form, so no translation is needed, only field selection (Handler
// is deliberately excluded — it is this package's own dispatch target, not caller-visible).
func describeExtensionRoutesJSON(routes []extensionRoute) []map[string]any {
	out := make([]map[string]any, 0, len(routes))
	for _, rt := range routes {
		m := map[string]any{
			"id":          rt.ID,
			"method":      rt.Method,
			"path":        rt.Path,
			"auth":        rt.Auth,
			"scope":       rt.Scope,
			"description": rt.Description,
		}
		if rt.RequestSchemaRef != "" {
			m["request_schema_ref"] = rt.RequestSchemaRef
		}
		if rt.ResponseSchemaRef != "" {
			m["response_schema_ref"] = rt.ResponseSchemaRef
		}
		out = append(out, m)
	}
	return out
}

// describeSchemas is the JSON-Schema-shaped `schemas` object §5.1's request_schema_ref/
// response_schema_ref index into. Deliberately not exhaustive JSON-Schema (no $ref resolution,
// no format validators) — enough for a caller to construct a valid request body and know what
// shape to expect back, which is what "sufficient for discovery" (§5.1) requires.
func describeSchemas() map[string]any {
	str := map[string]any{"type": "string"}
	uuid := map[string]any{"type": "string", "format": "uuid"}
	boolT := map[string]any{"type": "boolean"}
	intT := map[string]any{"type": "integer"}
	return map[string]any{
		"accounts_create_request": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"user_id": uuid, "smtp_host": str, "smtp_port": intT, "smtp_username": str,
				"smtp_secret": str, "from_address": str, "from_name": str, "daily_limit": intT,
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
					},
				}},
			},
			"required": []string{"user_id", "accounts"},
		},
		"accounts_bulk_add_response": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"request_id": str,
				"results":    map[string]any{"type": "array"},
			},
		},
		"accounts_test_request": map[string]any{
			"type":       "object",
			"properties": map[string]any{"acknowledge_alignment_warning": boolT},
		},
		"accounts_test_response": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"verification":   map[string]any{"type": "object"},
				"from_alignment": map[string]any{"type": "object"},
			},
		},
		"test_send_response": map[string]any{
			"type":       "object",
			"properties": map[string]any{"job_id": str, "state": str},
		},
		"account_object": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": uuid, "user_id": uuid, "smtp_host": str, "smtp_username": str,
				"from_address": str, "has_credential": boolT, "disabled": boolT, "paused": boolT,
				"quarantined": boolT,
			},
		},
		"accounts_list_response": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"results":     map[string]any{"type": "array", "items": map[string]any{"$ref": "#/schemas/account_object"}},
				"next_cursor": str,
			},
		},
		"account_events_response": map[string]any{
			"type":       "object",
			"properties": map[string]any{"results": map[string]any{"type": "array"}},
		},
		"settings_object": map[string]any{
			"type":       "object",
			"properties": map[string]any{"routing_strategy": str},
		},
		"usage_response": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"user_id": uuid, "from": str, "to": str,
				"days": map[string]any{"type": "array"},
			},
		},
	}
}
