package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestEscapePromQLString(t *testing.T) {
	if got, want := EscapePromQLString(`a\\b"c`+"\n"), `a\\\\b\"c\n`; got != want {
		t.Errorf("EscapePromQLString() = %q, want %q", got, want)
	}
}

func TestRemoteSourceQueryAt(t *testing.T) {
	const query = `count by (__name__) ({__name__=~"envoy_.*"})`
	at := time.Date(2024, time.January, 2, 3, 4, 5, 500000000, time.UTC)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("query"); got != query {
			t.Errorf("query = %q, want %q", got, query)
		}
		if got := r.URL.Query().Get("time"); got != at.Format(time.RFC3339Nano) {
			t.Errorf("time = %q, want %q", got, at.Format(time.RFC3339Nano))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"__name__":"envoy_up"},"value":[1704164645.5,"3"]}]}}`))
	}))
	defer server.Close()

	samples, err := NewRemoteSource(server.URL).QueryAt(context.Background(), query, at)
	if err != nil {
		t.Fatalf("QueryAt() error = %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("len(samples) = %d, want 1", len(samples))
	}
	if got := samples[0].Metric["__name__"]; got != "envoy_up" {
		t.Errorf("metric name = %q, want envoy_up", got)
	}
	if samples[0].Value != 3 {
		t.Errorf("value = %v, want 3", samples[0].Value)
	}
	if !samples[0].Timestamp.Equal(time.Unix(1704164645, 500000000).UTC()) {
		t.Errorf("timestamp = %s, want %s", samples[0].Timestamp, time.Unix(1704164645, 500000000).UTC())
	}
}

func TestRemoteSourceQueryRejectsNonVector(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"scalar","result":[1700000000,"1"]}}`))
	}))
	defer server.Close()

	if _, err := NewRemoteSource(server.URL).Query(context.Background(), "1"); err == nil {
		t.Fatal("Query() error = nil, want non-vector result error")
	}
}
