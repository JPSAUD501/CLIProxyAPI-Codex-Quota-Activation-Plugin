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
	UnknownReason      string   `json:"unknown_reason,omitempty"`
	UsedPercent        float64  `json:"used_percent"`
	ResetAt            int64    `json:"reset_at"`
	ResetAfterSeconds  int64    `json:"reset_after_seconds"`
	LimitWindowSeconds int64    `json:"limit_window_seconds"`
	UsedTokens         int64    `json:"used_tokens"`
	RemainingTokens    int64    `json:"remaining_tokens"`
	LimitTokens        int64    `json:"limit_tokens"`
	TokenAccounting    bool     `json:"token_accounting_available"`
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
	rateRaw, ok := aliasedRaw(root, "rate_limit", "rateLimit")
	if !ok {
		return Snapshot{}, errors.New("rate_limit is missing")
	}
	var rate map[string]json.RawMessage
	if err := json.Unmarshal(rateRaw, &rate); err != nil {
		return Snapshot{}, errors.New("rate_limit is malformed")
	}
	primary := parseWindow(rate, "primary_window", "primaryWindow")
	secondary := parseWindow(rate, "secondary_window", "secondaryWindow")
	out := Snapshot{Primary: primary, Secondary: secondary}
	present := []Window{}
	for index, w := range []Window{primary, secondary} {
		if w.Presence == Unknown {
			prefix := "primary_window_"
			if index == 1 {
				prefix = "secondary_window_"
			}
			out.Reason = prefix + w.UnknownReason
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
		if w.UsedPercent != 0 || (w.TokenAccounting && w.UsedTokens != 0) {
			out.Reason = "window_already_active"
			return out, nil
		}
		if w.LimitWindowSeconds <= 0 || w.ResetAfterSeconds <= 0 || w.ResetAt <= 0 {
			out.Reason = "missing_or_conflicting_fields"
			return out, nil
		}
		if w.TokenAccounting && (w.LimitTokens <= 0 || w.RemainingTokens != w.LimitTokens) {
			out.Reason = "conflicting_token_accounting"
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
func parseWindow(rate map[string]json.RawMessage, keys ...string) Window {
	raw, ok := aliasedRaw(rate, keys...)
	if !ok {
		return unknownWindow("missing")
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return Window{Presence: Absent}
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return unknownWindow("malformed")
	}
	w := Window{Presence: Present}
	var okAll bool
	w.UsedPercent, okAll = number(values, "used_percent", "usedPercent", "percent", "used")
	if !okAll {
		return unknownWindow("missing_or_invalid_used_percent")
	}
	if w.ResetAt, ok = integer(values, "reset_at", "resetAt"); !ok {
		return unknownWindow("missing_or_invalid_reset_at")
	}
	if w.ResetAfterSeconds, ok = integer(values, "reset_after_seconds", "resetAfterSeconds", "reset_in", "resetIn"); !ok {
		return unknownWindow("missing_or_invalid_reset_after")
	}
	if w.LimitWindowSeconds, ok = integer(values, "limit_window_seconds", "limitWindowSeconds", "window_seconds", "windowSeconds"); !ok {
		return unknownWindow("missing_or_invalid_window_duration")
	}
	used, usedPresent, usedValid := optionalInteger(values, "used_tokens", "usedTokens", "used_token_count", "usedTokenCount", "used_count", "usedCount")
	remaining, remainingPresent, remainingValid := optionalInteger(values, "remaining_tokens", "remainingTokens", "remaining_token_count", "remainingTokenCount", "remaining", "remaining_count", "remainingCount", "available_tokens", "availableTokens")
	limit, limitPresent, limitValid := optionalInteger(values, "limit_tokens", "limitTokens", "quota_tokens", "quotaTokens", "total_tokens", "totalTokens", "limit", "quota", "total")
	if !usedValid || !remainingValid || !limitValid {
		return unknownWindow("invalid_token_accounting")
	}
	if usedPresent || remainingPresent || limitPresent {
		if !usedPresent || !remainingPresent || !limitPresent {
			return unknownWindow("partial_token_accounting")
		}
		w.UsedTokens, w.RemainingTokens, w.LimitTokens = used, remaining, limit
		w.TokenAccounting = true
	}
	return w
}
func unknownWindow(reason string) Window {
	return Window{Presence: Unknown, UnknownReason: reason}
}

func aliasedRaw(values map[string]json.RawMessage, keys ...string) (json.RawMessage, bool) {
	var selected json.RawMessage
	found := false
	for _, key := range keys {
		raw, ok := values[key]
		if !ok {
			continue
		}
		if found && !bytes.Equal(bytes.TrimSpace(selected), bytes.TrimSpace(raw)) {
			return nil, false
		}
		selected, found = raw, true
	}
	return selected, found
}

func number(values map[string]json.RawMessage, keys ...string) (float64, bool) {
	var selected float64
	found := false
	for _, key := range keys {
		raw, ok := values[key]
		if !ok {
			continue
		}
		var n json.Number
		if json.Unmarshal(raw, &n) != nil {
			return 0, false
		}
		value, err := n.Float64()
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 100 || (found && value != selected) {
			return 0, false
		}
		selected, found = value, true
	}
	return selected, found
}

func integer(values map[string]json.RawMessage, keys ...string) (int64, bool) {
	value, present, valid := optionalInteger(values, keys...)
	return value, present && valid
}

func optionalInteger(values map[string]json.RawMessage, keys ...string) (int64, bool, bool) {
	var selected int64
	found := false
	for _, key := range keys {
		raw, ok := values[key]
		if !ok {
			continue
		}
		var n json.Number
		if json.Unmarshal(raw, &n) != nil {
			return 0, true, false
		}
		value, err := n.Int64()
		if err != nil || value < 0 || (found && value != selected) {
			return 0, true, false
		}
		selected, found = value, true
	}
	return selected, found, true
}
