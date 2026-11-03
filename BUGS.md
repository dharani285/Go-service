# Debugging findings

The debugging program had four defects against `debugging/README.md`.

## 1. Event de-duplication was scoped to one batch

- **Bug:** `seen` was allocated inside `processBatch`, so an event ID repeated in a later batch was counted again.
- **Why:** The input is processed in batches of 200, but the specification requires uniqueness across the entire file.
- **Fix:** Move the set to package scope as `seenEventIDs`, preserving it for the complete run. The main goroutine owns this map, so no additional lock is needed.
- **Verification:** Ran `go run . events.jsonl` and compared stdout with `expected_output.txt`; `diff` was empty.

## 2. Unique opens were keyed only by contact ID

- **Bug:** `openedBy[ev.ContactID]` treated a contact opening any campaign as already counted for every other campaign.
- **Why:** The requirement defines distinct contacts **per campaign**, not globally.
- **Fix:** Use a composite key of `campaign_id + NUL + contact_id` before checking/inserting into `openedBy`.
- **Verification:** Exact expected-output comparison passed; the per-campaign unique-open totals now match the fixture.

## 3. Daily delivered buckets used local time

- **Bug:** `ev.Timestamp.Local()` converted the UTC timestamp to the machine's local timezone before extracting the date.
- **Why:** The specification explicitly requires UTC calendar dates. Results could differ across machines/timezones and near midnight.
- **Fix:** Use `ev.Timestamp.UTC().Format("2006-01-02")`.
- **Verification:** Exact daily bucket output matches `expected_output.txt`.

## 4. Worker goroutines raced while incrementing counters

- **Bug:** Multiple workers incremented plain integer fields in the same `CampaignStats` concurrently.
- **Why:** Go map values may be pointers to shared structs; concurrent `++` operations are read-modify-write operations and are not atomic. Updates could be lost and the race detector reported data races, making output nondeterministic.
- **Fix:** Add `statsMu` and lock around the counter updates in `apply`. The main goroutine finishes `track` before workers start, and each batch waits for all workers before the next batch, so the map/unique-open tracking remains single-owner.
- **Verification:** `go run -race . events.jsonl` completed without a race report. Repeated normal runs matched `expected_output.txt` exactly.
