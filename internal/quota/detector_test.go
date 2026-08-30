package quota

import (
	"testing"
	"time"
)

func TestFreshWindowEligible(t *testing.T) {
	now := time.Unix(1000, 0)
	s, err := ParseSnapshot([]byte(`{"rate_limit":{"primary_window":{"used_percent":0,"reset_at":4600,"reset_after_seconds":3600,"limit_window_seconds":3600,"used_tokens":0,"remaining_tokens":100,"limit_tokens":100},"secondary_window":null}}`), "a", now)
	if err != nil || !s.Eligible || s.Secondary.Presence != Absent {
		t.Fatalf("%#v %v", s, err)
	}
}
func TestMissingWindowIsUnknownAndClosed(t *testing.T) {
	s, err := ParseSnapshot([]byte(`{"rate_limit":{"secondary_window":null}}`), "a", time.Now())
	if err != nil || s.Eligible || s.Primary.Presence != Unknown {
		t.Fatalf("%#v %v", s, err)
	}
}
func TestUsedWindowNotEligible(t *testing.T) {
	now := time.Unix(1000, 0)
	s, _ := ParseSnapshot([]byte(`{"rate_limit":{"primary_window":{"used_percent":1,"reset_at":4600,"reset_after_seconds":3600,"limit_window_seconds":3600,"used_tokens":1,"remaining_tokens":99,"limit_tokens":100},"secondary_window":null}}`), "a", now)
	if s.Eligible || s.Reason != "window_already_active" {
		t.Fatalf("%#v", s)
	}
}
