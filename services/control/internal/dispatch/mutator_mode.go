package dispatch

import "log"

// mutator_mode.go — the AUTHORITATIVE shadow ladder for this module's autonomous mutators
// (cross-alignment fix-plan item 13, send_byo_0053, ADR-0018).
//
// WHAT A MUTATOR MODE IS. Every autonomous writer — a write nobody asked for, taken by the Control on
// its own initiative — must be observable before it is allowed to act. M1 (coldmail-verify) has had
// this property from the start: all four of its mutators default to `recommend_only`, computing the
// decision and recording the recommendation while mutating nothing. This module had it for exactly one
// writer (auto-quarantine, send_byo_0051) and this file generalizes it.
//
//	off      the decision is not evaluated at all. No write, no observation, no log line.
//	shadow   the decision IS evaluated and RECORDED, and the write is NOT performed. (DEFAULT.)
//	enforce  the decision is evaluated and the write is performed. The pre-0053 behaviour.
//
// The ordering is off < shadow < enforce by ACTIVITY: each does strictly more than the one before.
// That direction is the opposite of rung.go's severity ordering, and the two must not be confused —
// hence a separate file, a separate table, and separate names rather than reusing the rung machinery.
//
// FOUR DIALS, NOT TWO. Module 2's equivalent file carries two (autopause, retention). This module has
// two additional autonomous sweeps that module 2 does not run at all — campaign expiry (Phase 5 §5.3)
// and the account purge (Phase 3 §6.3) — and both were undialled. They are also the two most
// irreversible writers here: campaign expiry moves jobs to TERMINAL `failed`, and the purge
// hard-DELETEs an account together with its credential ciphertext. Porting module 2's dial list
// verbatim would have left them ungated, which is the shape of defect item 13 exists to remove.
//
// WHY NOT REUSE rung.go's LADDER. They are inverse scales. A rung says how much the system STOPS
// (higher = does less); a mutator mode says how much the system ACTS (higher = does more). Sharing one
// severity map between them would put `stopped` and `enforce` at the same end of the scale, which is
// exactly backwards, and the composition rules would then read as opposites when they are in fact the
// same rule. Kept apart deliberately.
//
// WHY ONE FILE FOR ALL FOUR RATHER THAN A DIAL EACH. They are literally the same ladder: same three
// names, same ordering, same fail-safe target. Four copies of normalizeMutatorMode is how four dials
// drift into four slightly different meanings of `shadow`, one edit at a time. The per-dial part —
// which boolean composes, and what the observation IS — lives in the Effective*/predicate methods
// below, which is the only part that genuinely differs.

// MutatorMode is one rung of the off|shadow|enforce ladder. A string rather than an int, because it is
// what an operator types into the admin config PUT and what the config row stores.
type MutatorMode = string

const (
	// ModeOff evaluates nothing.
	ModeOff MutatorMode = "off"
	// ModeShadow decides and records, but does not write. The seeded default for every mutator.
	ModeShadow MutatorMode = "shadow"
	// ModeEnforce decides and writes.
	ModeEnforce MutatorMode = "enforce"
)

// mutatorActivity orders the ladder by HOW MUCH THE SYSTEM DOES. Used for the boolean-CEILING
// composition below; a plain string comparison would order them alphabetically (enforce < off <
// shadow), which is meaningless, so the comparison always goes through this table.
var mutatorActivity = map[MutatorMode]int{
	ModeOff:     0,
	ModeShadow:  1,
	ModeEnforce: 2,
}

// mutatorModeFallback is where an UNRECOGNIZED value lands: shadow, never enforce, never off.
//
// `enforce` would let a typo silently gain the power to act — the precise defect a shadow ladder
// exists to prevent, and in this module one of the four powers it would gain is DESTROYING A
// CREDENTIAL.
// `off` would silently disable a control an operator believes is running, which is M1's audit finding
// D2 (a control that looks normal while being ignored) reproduced here.
// `shadow` is the only value that both refuses to act AND keeps reporting, so it is the only correct
// fallback. This matches autoquarantine_mode's pre-existing fallback exactly, which is itself the
// reason this is a shared constant and not four separate literals.
const mutatorModeFallback = ModeShadow

// normalizeMutatorMode maps any stored value onto the ladder, logging a degradation at level=error for
// anything it does not recognize. Every mode read goes through here, so an unrecognized value cannot
// reach a gate site regardless of which dial it came from.
func normalizeMutatorMode(key, raw string) MutatorMode {
	switch raw {
	case ModeOff, ModeShadow, ModeEnforce:
		return raw
	default:
		log.Printf("dispatch: %s=%q unrecognized — falling back to %q (never enforce: a typo must not gain the power to act; never off: a silently disabled control is M1's D2 finding) level=error",
			key, raw, mutatorModeFallback)
		return mutatorModeFallback
	}
}

// EffectiveAutopauseMode is the ONLY way the auto-pause gate may read its dial.
//
// It composes the new `autopause_mode` ladder with the RETAINED `autopause_enabled` boolean as a
// CEILING: enabled=false CAPS the effective mode at `off`.
//
// WHY A CEILING HERE WHEN dispatch_mode COMPOSES ITS BOOLEAN AS A FLOOR (rung.go). Both are the same
// rule stated on inverse scales: THE BOOLEAN CAN ONLY EVER REDUCE WHAT THE SYSTEM DOES, NEVER INCREASE
// IT. On the rung ladder "does less" means a MORE severe rung, so the boolean is a floor. On the
// mutator ladder "does less" means a LESS active mode, so the boolean is a ceiling. Getting this
// backwards is the bug worth guarding against and it is asserted over the full matrix: an operator who
// set autopause_enabled=false must never receive an auto-pause, no matter what autopause_mode says.
//
// Note what the ceiling does NOT do: it never RAISES a mode. enabled=true with mode=shadow is shadow,
// not enforce. The boolean cannot promote a dial an operator left in observation.
func (c Config) EffectiveAutopauseMode() MutatorMode {
	mode := normalizeMutatorMode("autopause_mode", c.AutopauseMode)
	if !c.AutopauseEnabled && mutatorActivity[mode] > mutatorActivity[ModeOff] {
		return ModeOff
	}
	return mode
}

// EffectiveRetentionMode is the ONLY way the send_logs retention sweep may read its dial.
//
// retention_mode joins no boolean: `retention_sweep_interval_ms` and `retention_delete_batch_size` are
// RATE controls, not enables, so there is nothing to compose. It still goes through a method rather
// than being read from the struct directly, for one reason worth stating: the fail-safe normalization
// must be impossible to bypass. A gate that reads cfg.RetentionMode raw would silently treat a typo as
// "not enforce, so probably fine" — which happens to be right today and would be wrong the moment the
// ladder gains a rung.
func (c Config) EffectiveRetentionMode() MutatorMode {
	return normalizeMutatorMode("retention_mode", c.RetentionMode)
}

// EffectiveCampaignExpiryMode is the ONLY way the campaign expiry sweep may read its dial.
//
// Joins no boolean: `campaign_expiry_sec` is a THRESHOLD and
// `campaign_expiry_sweep_interval_ms` a RATE. Neither is an enable, so there is nothing to compose —
// and folding the threshold in would be worse than nothing, since "campaign_expiry_sec is huge" and
// "the sweep is off" are different states an operator needs to be able to tell apart.
func (c Config) EffectiveCampaignExpiryMode() MutatorMode {
	return normalizeMutatorMode("campaign_expiry_mode", c.CampaignExpiryMode)
}

// EffectiveAccountPurgeMode is the ONLY way the account purge sweep may read its dial.
//
// Joins no boolean. `account_retention_window_sec` is the window stamped into purge_after AT DELETE
// TIME by platformapi, not an enable for this sweep — and it must not be mistaken for one: setting it
// enormous does not stop the sweep, it only postpones each individual account, and rows already
// carrying a short purge_after are purged regardless. This dial is the only thing that stops the
// sweep itself.
func (c Config) EffectiveAccountPurgeMode() MutatorMode {
	return normalizeMutatorMode("account_purge_mode", c.AccountPurgeMode)
}

// --- the per-dial predicates -------------------------------------------------------------------
//
// Two per dial, and BOTH halves matter. The *Writes/*Deletes half gates the action; the
// *Decides/*Counts half gates the OBSERVATION, and it is true at shadow AND enforce. A shadow mode
// whose observation half was accidentally false would delete nothing and report nothing, which is
// `off` wearing another name — and an operator watching a log line that never comes would read the
// silence as "there is nothing to do".

// AutopauseWrites reports whether a would-pause decision may actually set accounts.paused.
func (c Config) AutopauseWrites() bool { return c.EffectiveAutopauseMode() == ModeEnforce }

// AutopauseDecides reports whether the decision is evaluated at all — true at shadow AND enforce,
// which is what makes shadow an OBSERVATION rather than an off switch.
func (c Config) AutopauseDecides() bool { return c.EffectiveAutopauseMode() != ModeOff }

// RetentionDeletes reports whether the send_logs sweep may issue its DELETE.
func (c Config) RetentionDeletes() bool { return c.EffectiveRetentionMode() == ModeEnforce }

// RetentionCounts reports whether the sweep should measure what it WOULD delete. True at shadow and
// enforce: at shadow the count IS the observation, and at enforce the deleted-row count serves the
// same purpose, so the sweep runs its query either way and only the DELETE is gated.
func (c Config) RetentionCounts() bool { return c.EffectiveRetentionMode() != ModeOff }

// CampaignExpiryExpires reports whether the sweep may mark a campaign expired and fail its jobs.
func (c Config) CampaignExpiryExpires() bool { return c.EffectiveCampaignExpiryMode() == ModeEnforce }

// CampaignExpiryDecides reports whether the sweep should identify the campaigns it WOULD expire. True
// at shadow and enforce. At shadow this runs the SAME candidate SELECT enforce runs — including the
// EXISTS term that excludes a naturally-completed campaign — because an observation of a different
// population than the one enforce would act on is worse than no observation: it would teach an
// operator to expect the wrong set.
func (c Config) CampaignExpiryDecides() bool { return c.EffectiveCampaignExpiryMode() != ModeOff }

// AccountPurgeDeletes reports whether the purge sweep may hard-DELETE an account row.
func (c Config) AccountPurgeDeletes() bool { return c.EffectiveAccountPurgeMode() == ModeEnforce }

// AccountPurgeCounts reports whether the sweep should measure how many accounts it WOULD purge. True
// at shadow and enforce. This is the observation an operator uses to decide whether
// account_retention_window_sec is set the way they think it is BEFORE any credential is destroyed,
// which is the whole reason the most irreversible writer in the module gets a ladder rather than an
// exclusion.
func (c Config) AccountPurgeCounts() bool { return c.EffectiveAccountPurgeMode() != ModeOff }
