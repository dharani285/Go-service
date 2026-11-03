package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestIngestIsIdempotentAndUniqueOpensArePerCampaign(t *testing.T) {
	s := NewStore()
	base := Event{EventID: "evt-1", CampaignID: "cmp-A", ContactID: "ct-1", Type: "opened"}
	if !s.Ingest(base) {
		t.Fatal("first event should be accepted")
	}
	if s.Ingest(base) {
		t.Fatal("duplicate event should not be accepted")
	}
	repeat := base
	repeat.EventID = "evt-2"
	if !s.Ingest(repeat) {
		t.Fatal("distinct event should be accepted")
	}
	otherCampaign := base
	otherCampaign.EventID = "evt-3"
	otherCampaign.CampaignID = "cmp-B"
	s.Ingest(otherCampaign)
	a, b := s.Stats("cmp-A"), s.Stats("cmp-B")
	if a.Opened != 2 || a.UniqueOpens != 1 {
		t.Fatalf("unexpected cmp-A stats: %+v", a)
	}
	if b.Opened != 1 || b.UniqueOpens != 1 {
		t.Fatalf("unexpected cmp-B stats: %+v", b)
	}
}

func TestPostEventsPartiallyAcceptsBatch(t *testing.T) {
	s := NewStore()
	body := `[{"event_id":"e1","campaign_id":"c1","contact_id":"u1","type":"sent","timestamp":"2026-08-10T09:00:00Z"},{"event_id":"e1","campaign_id":"c1","contact_id":"u1","type":"sent","timestamp":"2026-08-10T09:00:00Z"},{"event_id":"bad","campaign_id":"c1","contact_id":"u1","type":"bogus","timestamp":"2026-08-10T09:00:00Z"}]`
	req := httptest.NewRequest(http.MethodPost, "/events", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	handlePostEvents(s, rec, req)
	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Accepted   int `json:"accepted"`
		Duplicates int `json:"duplicates"`
		Rejected   int `json:"rejected"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Accepted != 1 || got.Duplicates != 1 || got.Rejected != 1 {
		t.Fatalf("unexpected result: %+v", got)
	}
	if s.Stats("c1").Sent != 1 {
		t.Fatal("event was double counted")
	}
}

func TestConcurrentIngestDoesNotDoubleCount(t *testing.T) {
	s := NewStore()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Ingest(Event{EventID: "same", CampaignID: "c", ContactID: "u", Type: "delivered"})
		}()
	}
	wg.Wait()
	if got := s.Stats("c").Delivered; got != 1 {
		t.Fatalf("delivered=%d, want 1", got)
	}
}
