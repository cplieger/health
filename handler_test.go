package health

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type stubSignal struct{ healthy bool }

func (s stubSignal) Healthy() bool { return s.healthy }

func TestHandler_Healthy(t *testing.T) {
	h := Handler(stubSignal{healthy: true})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var st Status
	if err := json.NewDecoder(rec.Body).Decode(&st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if st.Status != "OK" {
		t.Fatalf("status = %q, want OK", st.Status)
	}
	if st.Timestamp == "" {
		t.Fatal("timestamp is empty")
	}
}

func TestHandler_NilSignal(t *testing.T) {
	h := Handler(nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	var st Status
	if err := json.NewDecoder(rec.Body).Decode(&st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if st.Status != "Unavailable" {
		t.Fatalf("status = %q, want Unavailable", st.Status)
	}
	if st.Timestamp == "" {
		t.Fatal("timestamp is empty")
	}
}

func TestHandler_Unhealthy(t *testing.T) {
	h := Handler(stubSignal{healthy: false})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	var st Status
	if err := json.NewDecoder(rec.Body).Decode(&st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if st.Status != "Unavailable" {
		t.Fatalf("status = %q, want Unavailable", st.Status)
	}
}
