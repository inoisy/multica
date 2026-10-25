package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestModelForecastJSONUsesPoolTimePrecision(t *testing.T) {
	until := time.Date(2026, 10, 25, 12, 30, 59, 123456789, time.FixedZone("offset", 3600))
	choice := ModelChoice{WaitUntil: &until, Rows: []ModelRowState{{State: ModelRowLimited, Until: &until}}}
	raw, err := json.Marshal(choice)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), ".123") || strings.Count(string(raw), "2026-10-25T11:30:59Z") != 2 {
		t.Fatalf("forecast times must match pool RFC3339 precision: %s", raw)
	}
	if choice.WaitUntil.Nanosecond() != 123456789 || choice.Rows[0].Until.Nanosecond() != 123456789 {
		t.Fatal("serialization changed selection input")
	}
	var roundtrip ModelChoice
	if err := json.Unmarshal(raw, &roundtrip); err != nil || roundtrip.Rows[0].State != ModelRowLimited {
		t.Fatalf("roundtrip: %+v, %v", roundtrip, err)
	}
	without, _ := json.Marshal(ModelChoice{Rows: []ModelRowState{{State: ModelRowAvailable}}})
	if strings.Contains(string(without), "until") {
		t.Fatalf("nil forecast times must remain omitted: %s", without)
	}
}
