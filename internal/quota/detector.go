package quota

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"
)

type Presence string

const (
	Present Presence = "present"
	Absent  Presence = "absent"
	Unknown Presence = "unknown"
)

type Window struct {
	Presence           Presence `json:"presence"`
	UsedPercent        float64  `json:"used_percent"`
	ResetAt            int64    `json:"reset_at"`
	ResetAfterSeconds  int64    `json:"reset_after_seconds"`
	LimitWindowSeconds int64    `json:"limit_window_seconds"`
	UsedTokens         int64    `json:"used_tokens"`
	RemainingTokens    int64    `json:"remaining_tokens"`
	LimitTokens        int64    `json:"limit_tokens"`
}
type Snapshot struct {
	Primary   Window `json:"primary"`
	Secondary Window `json:"secondary"`
	Eligible  bool   `json:"eligible"`
	Reason    string `json:"reason"`
	CycleID   string `json:"cycle_id"`
}

func ParseSnapshot(raw []byte, accountKey string, now time.Time) (Snapshot, error) {
	var root map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil {
		return Snapshot{}, errors.New("malformed quota payload")
	}
	rateRaw, ok := root["rate_limit"]
	if !ok {
		return Snapshot{}, errors.New("rate_limit is missing")
	}
	var rate map[string]json.RawMessage
	if err := json.Unmarshal(rateRaw, &rate); err != nil {
		return Snapshot{}, errors.New("rate_limit is malformed")
	}
	primary := parseWindow(rate, "primary_window")
	secondary := parseWindow(rate, "secondary_window")
	out := Snapshot{Primary: primary, Secondary: secondary}
	present := []Window{}
	for _, w := range []Window{primary, secondary} {
		if w.Presence == Unknown {
			out.Reason = "unknown_window"
			return out, nil
		}
		if w.Presence == Present {
			present = append(present, w)
		}
	}
	if len(present) == 0 {
		out.Reason = "no_present_window"
		return out, nil
	}
	for _, w := range present {
		if w.UsedPercent != 0 || w.UsedTokens != 0 {
			out.Reason = "window_already_active"
			return out, nil
		}
		if w.LimitWindowSeconds <= 0 || w.ResetAfterSeconds <= 0 || w.ResetAt <= 0 || w.LimitTokens <= 0 || w.RemainingTokens != w.LimitTokens {
			out.Reason = "missing_or_conflicting_fields"
			return out, nil
		}
		delta := w.ResetAfterSeconds - w.LimitWindowSeconds
		if delta < 0 {
			delta = -delta
		}
		if delta > 60 {
			out.Reason = "window_not_fresh"
			return out, nil
		}
		if math.Abs(float64(w.ResetAt-now.Unix()-w.ResetAfterSeconds)) > 90 {
			out.Reason = "stale_reset_clock"
			return out, nil
		}
	}
	out.Eligible = true
	h := sha256.New()
	fmt.Fprintf(h, "%s|", accountKey)
	for _, w := range present {
		opened := w.ResetAt - w.LimitWindowSeconds
		fmt.Fprintf(h, "%d:%d|", w.LimitWindowSeconds, opened/60)
	}
	out.CycleID = hex.EncodeToString(h.Sum(nil))
	return out, nil
}
func parseWindow(rate map[string]json.RawMessage, key string) Window {
	raw, ok := rate[key]
	if !ok {
		return Window{Presence: Unknown}
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return Window{Presence: Absent}
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return Window{Presence: Unknown}
	}
	w := Window{Presence: Present}
	var okAll bool
	w.UsedPercent, okAll = number(values, "used_percent")
	if !okAll {
		return Window{Presence: Unknown}
	}
	if w.ResetAt, ok = integer(values, "reset_at"); !ok {
		return Window{Presence: Unknown}
	}
	if w.ResetAfterSeconds, ok = integer(values, "reset_after_seconds"); !ok {
		return Window{Presence: Unknown}
	}
	if w.LimitWindowSeconds, ok = integer(values, "limit_window_seconds"); !ok {
		return Window{Presence: Unknown}
	}
	if w.UsedTokens, ok = integer(values, "used_tokens"); !ok {
		return Window{Presence: Unknown}
	}
	if w.RemainingTokens, ok = integer(values, "remaining_tokens"); !ok {
		return Window{Presence: Unknown}
	}
	if w.LimitTokens, ok = integer(values, "limit_tokens"); !ok {
		return Window{Presence: Unknown}
	}
	return w
}
func number(values map[string]json.RawMessage, key string) (float64, bool) {
	raw, ok := values[key]
	if !ok {
		return 0, false
	}
	var n json.Number
	if json.Unmarshal(raw, &n) != nil {
		return 0, false
	}
	v, err := n.Float64()
	return v, err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= 100
}
func integer(values map[string]json.RawMessage, key string) (int64, bool) {
	raw, ok := values[key]
	if !ok {
		return 0, false
	}
	var n json.Number
	if json.Unmarshal(raw, &n) != nil {
		return 0, false
	}
	v, err := n.Int64()
	return v, err == nil && v >= 0
}
