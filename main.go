package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const maxBodyBytes = 2 << 20

// Store is process-local in-memory persistence. A single lock makes checking
// idempotency and updating aggregates one atomic operation.
type Store struct {
	mu         sync.RWMutex
	events     map[string]Event
	stats      map[string]*CampaignStats
	opened     map[string]map[string]struct{} // campaign -> distinct contacts
	byCampaign map[string][]Event
}

func NewStore() *Store {
	return &Store{events: map[string]Event{}, stats: map[string]*CampaignStats{}, opened: map[string]map[string]struct{}{}, byCampaign: map[string][]Event{}}
}

func (s *Store) Ingest(ev Event) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.events[ev.EventID]; exists {
		return false
	}
	s.events[ev.EventID] = ev
	cs := s.stats[ev.CampaignID]
	if cs == nil {
		cs = &CampaignStats{CampaignID: ev.CampaignID}
		s.stats[ev.CampaignID] = cs
	}
	switch ev.Type {
	case "sent":
		cs.Sent++
	case "delivered":
		cs.Delivered++
	case "opened":
		cs.Opened++
		contacts := s.opened[ev.CampaignID]
		if contacts == nil {
			contacts = map[string]struct{}{}
			s.opened[ev.CampaignID] = contacts
		}
		if _, ok := contacts[ev.ContactID]; !ok {
			contacts[ev.ContactID] = struct{}{}
			cs.UniqueOpens++
		}
	case "clicked":
		cs.Clicked++
	}
	s.byCampaign[ev.CampaignID] = append(s.byCampaign[ev.CampaignID], ev)
	return true
}

func (s *Store) Stats(campaignID string) CampaignStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if cs := s.stats[campaignID]; cs != nil {
		return *cs
	}
	return CampaignStats{CampaignID: campaignID}
}

func (s *Store) Events(campaignID string, limit, offset int) ([]Event, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	all := s.byCampaign[campaignID]
	total := len(all)
	if offset >= total {
		return []Event{}, total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	// Newest event timestamp first; event_id breaks ties deterministically.
	result := append([]Event(nil), all...)
	sort.Slice(result, func(i, j int) bool {
		if result[i].Timestamp.Equal(result[j].Timestamp) {
			return result[i].EventID > result[j].EventID
		}
		return result[i].Timestamp.After(result[j].Timestamp)
	})
	return result[offset:end], total
}

func main() {
	store := NewStore()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /events", func(w http.ResponseWriter, r *http.Request) { handlePostEvents(store, w, r) })
	mux.HandleFunc("GET /campaigns/{campaignID}/stats", func(w http.ResponseWriter, r *http.Request) { handleGetStats(store, w, r) })
	mux.HandleFunc("GET /campaigns/{campaignID}/events", func(w http.ResponseWriter, r *http.Request) { handleGetEvents(store, w, r) })
	addr := ":8080"
	if port := os.Getenv("PORT"); port != "" {
		addr = ":" + port
	}
	log.Printf("campaign events API listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

type ingestResult struct {
	Index   int    `json:"index"`
	EventID string `json:"event_id,omitempty"`
	Status  string `json:"status"`
	Error   string `json:"error,omitempty"`
}

func handlePostEvents(s *Store, w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large or unreadable"})
		return
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil || items == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expected a JSON array of events"})
		return
	}
	results := make([]ingestResult, 0, len(items))
	accepted, duplicates, rejected := 0, 0, 0
	for i, item := range items {
		ev, err := decodeEvent(item)
		if err != nil {
			rejected++
			results = append(results, ingestResult{Index: i, Status: "rejected", Error: err.Error()})
			continue
		}
		if s.Ingest(ev) {
			accepted++
			results = append(results, ingestResult{Index: i, EventID: ev.EventID, Status: "accepted"})
		} else {
			duplicates++
			results = append(results, ingestResult{Index: i, EventID: ev.EventID, Status: "duplicate"})
		}
	}
	status := http.StatusOK
	if rejected > 0 {
		status = http.StatusMultiStatus
	}
	writeJSON(w, status, map[string]any{"accepted": accepted, "duplicates": duplicates, "rejected": rejected, "results": results})
}

func decodeEvent(raw json.RawMessage) (Event, error) {
	var ev Event
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&ev); err != nil {
		return ev, errors.New("invalid event JSON or field type")
	}
	if strings.TrimSpace(ev.EventID) == "" || strings.TrimSpace(ev.CampaignID) == "" || strings.TrimSpace(ev.ContactID) == "" {
		return ev, errors.New("event_id, campaign_id, and contact_id are required")
	}
	switch ev.Type {
	case "sent", "delivered", "opened", "clicked":
	default:
		return ev, errors.New("type must be sent, delivered, opened, or clicked")
	}
	if ev.Timestamp.IsZero() {
		return ev, errors.New("timestamp is required and must be RFC3339")
	}
	return ev, nil
}

func handleGetStats(s *Store, w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Stats(r.PathValue("campaignID")))
}

func handleGetEvents(s *Store, w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit", 50)
	if err != nil || limit < 1 || limit > 200 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "limit must be between 1 and 200"})
		return
	}
	offset, err := queryInt(r, "offset", 0)
	if err != nil || offset < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "offset must be a non-negative integer"})
		return
	}
	events, total := s.Events(r.PathValue("campaignID"), limit, offset)
	writeJSON(w, http.StatusOK, map[string]any{"campaign_id": r.PathValue("campaignID"), "total": total, "limit": limit, "offset": offset, "events": events})
}
func queryInt(r *http.Request, key string, def int) (int, error) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def, nil
	}
	return strconv.Atoi(v)
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write JSON: %v", err)
	}
}
