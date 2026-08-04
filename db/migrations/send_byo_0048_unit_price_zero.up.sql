-- send_byo_0048_unit_price_zero.up.sql — M3-P7-OD-11 RULED 2026-08-02.
--
-- THE DEFECT, as found while starting Phase 7 C3 (quote as a feasibility check, §4). Module 3's
-- own build plan (module-3-build-plan.md §1) states plainly: "Pricing is a flat monthly
-- subscription... There is no per-send charge, no credits, and no per-recipient price." Phase 7
-- §4.1 repeats this and says the consequence: quote/describe's cost fields are retained (Rule 5 —
-- never removed) but become inert (cost:0, unit_price_credits:0) under this module's pricing. But
-- the row Phase 9.5's send_byo_0021 seeded (inherited from module 2, {"v":1}) is what the LIVE code
-- has been charging with — real, non-zero, per-recipient billing, contradicting the module's own
-- documented pricing model. Same defect class as M3-P7-OD-9/-10: two locked artifacts, never
-- diffed against each other.
--
-- WHY THIS IS A DELETE+RE-INSERT, NOT AN UPDATE (CLAUDE.md Rule 1: "default_value is never
-- mutated"). Mirrors M3-P5-OD-1's own precedent for quarantine_threshold, which faced the
-- identical shape (a key's MEANING changed, not merely its tuning) — delete the old row, seed a
-- fresh one at a NEW site with its own default. unit_price_credits keeps its name (Rule 5:
-- renaming would be a second contract break for no reason — the field still means "credits per
-- recipient," it is simply always zero under this module's actual pricing), but this migration is
-- its new seed site, and going forward THIS default_value is the one that is never mutated again.
DELETE FROM send_byo.config WHERE key = 'unit_price_credits';

INSERT INTO send_byo.config (key, value, default_value, description, value_type) VALUES
('unit_price_credits', '{"v":0}', '{"v":0}',
 'Credits charged per recipient (quote/describe). Module 3 prices as a flat monthly subscription
(module-3-build-plan.md §1) -- there is no per-recipient charge, so this is 0 by default. Retained
per Rule 5 for contract compatibility with quote/describe''s additive cost fields, which report 0
at this default rather than being removed. Re-seeded here (M3-P7-OD-11) from Phase 9.5''s send_byo_0021
row, whose inherited default of 1 contradicted this module''s own documented pricing model.',
 'integer');

INSERT INTO send_byo.schema_migrations (version) VALUES ('0048');
