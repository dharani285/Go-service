# Campaign Events API

A standard-library-only Go HTTP service that ingests provider events and exposes campaign statistics.

## Requirements

- Go 1.22 or later (tested with Go 1.23.2)
- No external Go dependencies

## Run

From this directory:

```bash
go test -race ./...
go run .
```

The API listens on `http://localhost:8080`. Set `PORT` to use another port (for example, `PORT=8081 go run .`).

## Endpoints

### `POST /events`

Accepts a JSON array. Each item is validated independently. Valid events are accepted even if another item in the same batch is invalid.

```bash
curl -i -X POST http://localhost:8080/events \
  -H 'Content-Type: application/json' \
  -d '[{"event_id":"evt_1","campaign_id":"cmp_summer_sale","contact_id":"ct_001","type":"delivered","timestamp":"2026-08-10T06:15:00Z"}]'
```

Response example:

```json
{
  "accepted": 1,
  "duplicates": 0,
  "rejected": 0,
  "results": [
    {"index": 0, "event_id": "evt_1", "status": "accepted"}
  ]
}
```

A fully processed batch returns `200`. A batch with one or more invalid items returns `207 Multi-Status`; a malformed top-level JSON body returns `400`. A request body is limited to 2 MiB.

### `GET /campaigns/{campaignID}/stats`

```bash
curl http://localhost:8080/campaigns/cmp_summer_sale/stats
```

Returns `campaign_id`, `sent`, `delivered`, `opened`, `clicked`, and `unique_opens`. Event counters count distinct accepted event IDs. `unique_opens` counts distinct contacts with at least one accepted open in that campaign. A campaign with no events returns zero counts.

### `GET /campaigns/{campaignID}/events`

Optional paginated recent activity endpoint:

```bash
curl 'http://localhost:8080/campaigns/cmp_summer_sale/events?limit=20&offset=0'
```

Default limit is 50; allowed limit is 1–200. Events are sorted newest timestamp first, with event ID as a deterministic tie-breaker.

## Event rules

- Required non-empty fields: `event_id`, `campaign_id`, `contact_id`.
- `type` must be `sent`, `delivered`, `opened`, or `clicked`.
- `timestamp` must be a valid RFC3339 timestamp.
- `metadata` is optional arbitrary JSON.
- `event_id` is the global idempotency key. The first valid event with an ID wins; later occurrences are returned as `duplicate`, including conflicting payloads.
- Events may arrive late or out of order. No lifecycle ordering is enforced.

## Persistence and limitations

This implementation uses in-memory maps protected by a mutex. It is safe for concurrent HTTP requests within one process, but data is lost on restart and is not shared between multiple service instances. For production, persist events with a unique constraint on `event_id` and update aggregates transactionally. See the scale memo in the repository-root `NOTES.md`.

## Load the supplied seed

From this directory:

```bash
curl -i -X POST http://localhost:8080/events \
  -H 'Content-Type: application/json' \
  --data-binary @seed/events.json
```

Malformed timestamps in the supplied seed are rejected per item; the remaining valid records are processed. Re-sending the file will not double count already accepted event IDs during the same process lifetime.
