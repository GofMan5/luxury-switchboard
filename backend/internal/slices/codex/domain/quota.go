package domain

import (
	"math"
	"time"
)

// QuotaWindow is one rate-limit window of the ChatGPT usage endpoint,
// normalised to the shape the UI can draw directly: how much of the
// window is left, how wide the window is, and when it reopens. All
// derived fields are absent when the endpoint did not report them — a
// zero WindowMinutes means "not reported", a zero ResetAt means the
// same — because inventing a width or a deadline the upstream never
// named would misdirect the owner's cooldown decisions.
type QuotaWindow struct {
	Present          bool
	RemainingPercent int
	WindowMinutes    int
	ResetAt          time.Time
}

// QuotaWindowFields is one window exactly as the usage endpoint reports
// it, before normalisation. Every field is optional: the endpoint is
// best-effort and omits whatever it does not want to state. Reported
// carries the only fact the fields themselves cannot express — whether
// the window object arrived at all — because a window that was not
// reported is a different statement from a reported window with no
// numbers in it.
type QuotaWindowFields struct {
	Reported           bool
	UsedPercent        *int64
	LimitWindowSeconds *int64
	ResetAt            *int64
	ResetAfterSeconds  *int64
}

// Usage is the account side of the usage endpoint: the plan it names
// and the two rate-limit windows the quota card shows. It carries no
// secrets — percentages, seconds and epochs only — so it may cross the
// stdio boundary as-is.
type Usage struct {
	PlanType  string
	Primary   QuotaWindow
	Secondary QuotaWindow
}

// NormalizeQuotaWindow ports the reference implementation's maths so
// the numbers agree with what the cockpit showed for the same account:
//
//   - remaining is 100 minus the clamped usage — a window the upstream
//     over- or under-reports cannot leave the 0..100 range the meter draws;
//   - a window length rounds up to whole minutes, so "59 s" reads as
//     the 1-minute window it behaves as;
//   - the reset prefers the absolute epoch and falls back to "now plus
//     the reported offset"; a non-positive or missing deadline is an
//     absent deadline, not one already in the past.
//
// A window that did not arrive reports itself absent with a full
// meter: "no limit reported" must not render as "all spent", which is
// what a zero remaining would say.
func NormalizeQuotaWindow(fields QuotaWindowFields, now time.Time) QuotaWindow {
	if !fields.Reported {
		return QuotaWindow{Present: false, RemainingPercent: 100}
	}
	used := 0
	if fields.UsedPercent != nil {
		used = int(*fields.UsedPercent)
	}
	used = min(max(used, 0), 100)
	window := QuotaWindow{Present: true, RemainingPercent: 100 - used}
	if fields.LimitWindowSeconds != nil && *fields.LimitWindowSeconds > 0 {
		window.WindowMinutes = int(math.Ceil(float64(*fields.LimitWindowSeconds) / 60))
	}
	if fields.ResetAt != nil && *fields.ResetAt > 0 {
		window.ResetAt = time.Unix(*fields.ResetAt, 0).UTC()
	} else if fields.ResetAfterSeconds != nil && *fields.ResetAfterSeconds >= 0 {
		window.ResetAt = now.Add(time.Duration(*fields.ResetAfterSeconds) * time.Second)
	}
	return window
}
