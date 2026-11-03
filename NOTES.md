# Engineering notes — Campaign Events

## Part 1 — Understanding the problem

Relay receives lifecycle events from delivery providers and exposes campaign-level counts to a dashboard. Providers retry requests, so ingestion must be idempotent. Events may arrive late or out of order, so the service must count valid event facts rather than assume a strict `sent -> delivered -> opened -> clicked` arrival sequence.

### Assumptions

- `event_id` is globally unique across providers and campaigns, as the brief says it identifies one event. It is the idempotency key.
- The first valid event accepted for an `event_id` wins. A later event with the same ID is treated as a duplicate, even if its payload differs. The API does not overwrite the original.
- Each distinct accepted event increments exactly one of `sent`, `delivered`, `opened`, or `clicked` for its campaign. Events are not required to arrive in lifecycle order.
- `unique_opens` means distinct `contact_id` values with at least one accepted `opened` event within that campaign. The same contact in two campaigns counts once in each.
- `timestamp` must be RFC3339 (including an offset or `Z`). It is event time, not receive time. Stats currently aggregate over all retained events; no time window is implied.
- `metadata` is optional arbitrary JSON. It is retained with the event but does not affect counters.
- A valid event needs non-empty `event_id`, `campaign_id`, and `contact_id`, and one of the four documented event types.
- Unknown fields are ignored for forward compatibility. Invalid items are rejected independently; valid items in the same batch are still accepted.

### Ambiguities / questions for the PM

- Is `event_id` globally unique, or only unique per provider? If per-provider, the provider identity must be part of the idempotency key.
- If the same ID arrives with a different payload, should we keep the first, reject and alert, or support provider corrections?
- Are counters raw event counts or unique-message counts? Can one message have multiple opened/clicked events?
- Should dashboard stats be lifetime totals, campaign-window totals, or selectable date ranges? Which timezone defines reporting days?
- Should invalid events be retried by the provider? What response/body contract do providers expect for mixed-validity batches?
- Are there provider-specific event types, delivery channels, or metadata schemas to preserve?
- How long must raw events be retained, and are deletion/privacy requirements applicable?
- Do we need authentication, provider-specific rate limits, signatures, or replay protection? Excluded by the assignment's explicit scope.
- Should events with timestamps far in the future or very old timestamps be accepted and flagged?

### Priorities

1. Make ingestion idempotent and safe under concurrent requests.
2. Validate each event independently so one malformed item cannot discard a batch.
3. Make campaign counters and unique-open semantics explicit.
4. Add tests for duplicates, partial batches, campaign-scoped unique opens, and concurrent ingestion.
5. Add a small paginated recent-events endpoint if core functionality is stable.
6. Document trade-offs and scale path; avoid deployment/UI work.

## Part 4 — Scale memo: 100 million events/day

100 million/day averages about 1,157 events/second, with peaks likely much higher. The current in-memory implementation fails first on durability and memory, not raw HTTP syntax: a restart loses every event and counter, and the global event map plus retained events grow without bound. One process and one mutex also become a throughput bottleneck; a single machine cannot be assumed to absorb peak traffic.

### Change order

1. Measure real request sizes, peak events/second, latency, duplicate rate, and campaign skew. Set explicit SLOs and retention requirements.
2. Move raw events and idempotency keys to durable storage. Use a unique constraint on the idempotency key and transactionally persist the event plus aggregate update (or use a transactional outbox).
3. Partition/shard by a stable key (for example campaign or event time) based on query and write patterns. Add indexes for campaign/time and event ID.
4. Separate ingestion from dashboard reads using materialized aggregate tables or a streaming projection. Keep raw events as the auditable source of truth.
5. Horizontally scale stateless API instances; add backpressure, request limits, batching, and load shedding.

### API and storage

The basic `POST /events` contract can remain, but at this scale I would formalize per-item outcomes, provider identity, maximum batch size, retry semantics, and an idempotency scope. Stats may need date-range and timezone parameters plus an explicit `as_of`/freshness field if projections are eventually consistent. Storage must change from process memory to durable, horizontally scalable storage. A relational database with partitioning and unique constraints is a reasonable first step; a stream/log plus analytical store may become appropriate after measured limits.

### Queue trade-offs and duplicate paths

A durable queue (Kafka/Pulsar/SQS-like) can absorb bursts and decouple provider latency from aggregation. It adds consumer lag, at-least-once delivery, poison messages, replay/reordering, partition hot spots, schema evolution, operational burden, and a need to communicate eventual consistency. Duplicates can appear at HTTP retries, queue producer retries, broker redelivery, consumer restarts, replay/backfill, and projection retries. Idempotency must therefore exist at the durable write/projection boundary, not only in the HTTP process.

### Keeping counters trustworthy

Keep immutable raw events, enforce uniqueness at the database boundary, and make aggregate updates atomic/idempotent. Rebuild aggregates from raw events and compare them with live projections. Track reconciliation deltas, duplicate/conflict counts, invalid-event counts, and late-arrival distributions. Define whether corrections are new compensating events or mutations with audit history. Never rely solely on an in-memory counter as the source of truth.

### Monitoring

- Ingestion request rate, event rate, batch size, 2xx/4xx/5xx, latency percentiles.
- Accepted, duplicate, conflicting-ID, invalid, and unknown-type rates by provider.
- Queue depth/consumer lag and oldest unprocessed event age.
- Database write latency, connection saturation, lock contention, storage growth, index health.
- Projection freshness, reconciliation mismatch, aggregate rebuild duration.
- Campaign-level anomalies and provider timestamp vs receive-time delay.

### Deliberately defer

I would not introduce multi-region active-active writes, complex stream processing, a dedicated OLAP warehouse, or a custom exactly-once protocol before measurements justify them. I would first make writes durable and idempotent, measure bottlenecks, and scale the simplest architecture that meets the SLO.

## Part 5 — The angry marketer

A delivered count rising from 970,000 to 975,000 over 30 minutes is not inherently suspicious. Providers retry and events arrive late; newly received valid delivered events should increase a cumulative count.

Plausible explanations for opened falling from 250,000 to 248,000 include:

- The dashboard's two values are not the same metric or query: one view may show total opens and another unique opens, or filters/time ranges differ.
- A rolling time window moved forward, so older opens left the window while newer deliveries arrived.
- A late backfill/reconciliation recomputed counts after duplicate removal or invalid-event filtering.
- Duplicate event IDs were previously counted and later deduplicated.
- Provider corrections, event retractions, or data-quality cleanup removed invalid opens.
- A campaign/contact merge, suppression, privacy deletion, or bot/open filtering changed the eligible population.
- A timezone boundary or timestamp parsing fix moved events between reporting windows.
- The dashboard reads from eventually consistent replicas/projections at different refresh points.
- A cache was invalidated or one request read stale/cached data while another read a newer projection.
- A query, deployment, or aggregation bug changed filters, joins, grouping, or deduplication semantics.
- The marketer may be comparing different campaign selections, channel filters, or date ranges.

### What I would check first

1. Confirm campaign ID, date range, timezone, filters, and whether `opened` means raw opens or unique contacts in both screenshots/API requests.
2. Compare raw event IDs and aggregate query results at both `as_of` times. Identify exactly which event IDs were added or removed.
3. Check deploys, backfills, deduplication jobs, provider corrections, cache refreshes, and projection lag between 10:00 and 10:30.
4. Recompute the metric from immutable raw events using the documented rule and compare it with the dashboard.
5. Check whether the same event ID has conflicting payloads and whether any events were deleted or filtered.

I would call it a bug if the same metric definition, campaign, window, and source data produce different totals without an explained correction or window movement. I would call it expected if the metric is a rolling window, unique count, or a corrected projection and the changed event set explains the delta. The delivered increase is expected for a cumulative all-time metric with late arrivals; it is suspicious only if the query is meant to be a fixed, finalized snapshot or if deliveries are being counted more than once.

## What I completed / what I intentionally skipped

- Completed: in-memory Go API for batch ingestion, per-item validation/results, global event-ID deduplication, campaign stats, campaign-scoped unique opens, and paginated event history.
- Completed: tests for idempotency, partial batch handling, unique-open scope, and concurrent duplicate requests.
- Completed: all four debugging fixes and verified exact output against `debugging/expected_output.txt`, including the race detector.
- Skipped: persistent SQLite storage, authentication/signature verification, provider-specific schemas, frontend, deployment, and production observability. These are outside the core time-boxed scope; in-memory storage is intentionally a demo trade-off and loses data on restart.

## If I had another day

I would add SQLite persistence with a unique index on `event_id`, transactionally update aggregates, add tests for restart durability and conflicting duplicate payloads, and define a stable API contract for date-windowed stats and provider identity. I would also add request-level metrics and fuzz tests for malformed payloads.
