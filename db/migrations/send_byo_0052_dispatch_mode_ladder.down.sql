-- send_byo_0052_dispatch_mode_ladder.down.sql — remove the dispatch_mode row.
--
-- This down is the clean direction: the key is NEW here, so removing the row restores the exact
-- pre-0052 state of send_byo.config. Nothing else was added, altered or granted, so there is nothing
-- else to reverse.
--
-- WHAT THIS DOWN CANNOT DO, and why that direction is the DANGEROUS one here (the asymmetry is the
-- opposite of send_byo_0051's, so it is stated rather than assumed). 0051 dropped a row whose Go
-- reader came out in the same commit, and noted that restoring the row without the field left an inert
-- row — harmless. Here the Go side is a READ: dispatch/config.go loads dispatch_mode with
-- readStringKey, which errors when the row is absent, and LoadConfig is called at Control boot
-- (cmd/control/main.go), on the config-reload path, and by the admin surface. So running this down
-- WITHOUT reverting the Go source leaves a Control that does not start.
--
-- That is the same coupling 0051's up file called out in the other direction, and the same disposition
-- applies: this down belongs to a source revert, not to a standalone `psql -f`. Whoever runs it as part
-- of reverting the item-12 commit gets the reader removed by git in the same step. Whoever runs it
-- standalone gets a hard, immediate, loud boot failure — which is the correct failure for "the code
-- requires a dial the database does not have," and is emphatically better than the alternative a
-- graceful default would produce: a Control that boots and silently ignores the rung an operator
-- pulled. A read that fails loudly is not a defect to be smoothed over here.

DELETE FROM send_byo.config WHERE key = 'dispatch_mode';

DELETE FROM send_byo.schema_migrations WHERE version = '0052';
