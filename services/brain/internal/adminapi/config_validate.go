package adminapi

import (
	"bytes"
	"encoding/json"
	"time"

	// Embed the IANA tz database so daily_reset_timezone validation (time.LoadLocation) works
	// regardless of the container's system tzdata. This is a data dependency, not a tuning
	// constant (config-not-code is about behavioral dials; the tz table is a validation source).
	_ "time/tzdata"
)

// validateConfigValue enforces value_type (§6.3) and per-key semantic rules (§6.4) BEFORE write.
// The DB stores opaque JSONB, so these checks are the only guard against a bad dial; each failure
// is a clean envelope (never a raw error). value is the UNWRAPPED scalar the client sent.
func validateConfigValue(key, valueType string, value json.RawMessage) *apiError {
	// --- value_type gate ---
	switch valueType {
	case "integer":
		if _, ok := asInt(value); !ok {
			return typeErr(key, issueMalformed, "config_value_not_integer")
		}
	case "string":
		var s string
		if json.Unmarshal(value, &s) != nil {
			return typeErr(key, issueMalformed, "config_value_not_string")
		}
	case "boolean":
		var b bool
		if json.Unmarshal(value, &b) != nil {
			return typeErr(key, issueMalformed, "config_value_not_boolean")
		}
	case "uuid":
		// string matching UUID, or null.
		if string(value) != "null" {
			var s string
			if json.Unmarshal(value, &s) != nil || !isUUID(s) {
				return typeErr(key, issueMalformed, "config_value_not_uuid")
			}
		}
	case "array<integer>":
		var arr []json.Number
		if json.Unmarshal(value, &arr) != nil {
			return typeErr(key, issueMalformed, "config_value_not_int_array")
		}
		for _, n := range arr {
			if _, err := n.Int64(); err != nil {
				return typeErr(key, issueMalformed, "config_value_not_int_array")
			}
		}
	default:
		// Unknown value_type in the seed contract — reject rather than write blindly.
		return typeErr(key, issueMalformed, "config_unknown_value_type:"+valueType)
	}

	// --- per-key semantic validation (§6.4) ---
	switch key {
	case "daily_reset_timezone":
		var tz string
		_ = json.Unmarshal(value, &tz)
		if _, err := time.LoadLocation(tz); err != nil {
			return typeErr(key, issueForbiddenValue, "config_invalid_iana_tz:"+tz)
		}
	case "daily_reset_hour":
		if n, ok := asInt(value); !ok || n < 0 || n > 23 {
			return typeErr(key, issueOutOfRange, "config_daily_reset_hour_out_of_range")
		}
	case "quarantine_threshold":
		if n, ok := asInt(value); !ok || n < 0 || n > 100 {
			return typeErr(key, issueOutOfRange, "config_quarantine_threshold_out_of_range")
		}
	case "daily_limit_default":
		// > 0 and <= the same hardcoded sanity ceiling as per-account daily_limit (§6.4 says the
		// default is bounded by "the ceiling used to sanity-bound per-account daily_limit in §3").
		// OD-24 option (c): a bound, not a config dial.
		if n, ok := asInt(value); !ok || n <= 0 || n > DailyLimitCeiling {
			return typeErr(key, issueOutOfRange, "config_daily_limit_default_out_of_range")
		}
	}
	return nil
}

// asInt reports whether raw is a JSON integer (no fractional part) and returns it.
func asInt(raw json.RawMessage) (int64, bool) {
	var num json.Number
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&num); err != nil {
		return 0, false
	}
	n, err := num.Int64()
	if err != nil {
		return 0, false
	}
	return n, true
}

// typeErr builds a config validation_failed envelope with a field detail keyed on "value".
func typeErr(key, issue, internalReason string) *apiError {
	return newErr(codeValidationFailed, internalReason).
		withDetails(fieldDetail{Field: "value", Issue: issue})
}
