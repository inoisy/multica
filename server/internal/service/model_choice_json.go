package service

import (
	"encoding/json"
	"time"
)

// Forecasts share the second precision used by pool availability responses.
// Only serialization changes; selection still compares the original timestamps.
func forecastTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	value := t.UTC().Format(time.RFC3339)
	return &value
}

func (r ModelRowState) MarshalJSON() ([]byte, error) {
	type row ModelRowState
	return json.Marshal(struct {
		row
		Until *string `json:"until,omitempty"`
	}{row(r), forecastTime(r.Until)})
}

func (c ModelChoice) MarshalJSON() ([]byte, error) {
	type choice ModelChoice
	return json.Marshal(struct {
		choice
		WaitUntil *string `json:"wait_until,omitempty"`
	}{choice(c), forecastTime(c.WaitUntil)})
}
