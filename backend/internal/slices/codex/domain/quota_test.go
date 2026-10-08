package domain

import (
	"reflect"
	"testing"
	"time"
)

func TestNormalizeQuotaWindow(t *testing.T) {
	now := time.Unix(1_750_000_000, 0).UTC()
	ptr := func(value int64) *int64 { return &value }

	// The rule the reference implementation pinned: the meter only ever
	// draws 0..100, sub-minute windows round up to the minute they
	// behave as, and a deadline the upstream did not state is absent
	// rather than already spent or already passed.
	cases := []struct {
		name   string
		fields QuotaWindowFields
		now    time.Time
		want   QuotaWindow
	}{
		{
			"a window that never arrived reports absent with a full meter",
			QuotaWindowFields{}, now,
			QuotaWindow{Present: false, RemainingPercent: 100},
		},
		{
			"a reported window without numbers is present and untouched",
			QuotaWindowFields{Reported: true}, now,
			QuotaWindow{Present: true, RemainingPercent: 100},
		},
		{
			"usage below zero clamps to a full meter",
			QuotaWindowFields{Reported: true, UsedPercent: ptr(-7)}, now,
			QuotaWindow{Present: true, RemainingPercent: 100},
		},
		{
			"usage above 100 clamps to an empty meter",
			QuotaWindowFields{Reported: true, UsedPercent: ptr(250)}, now,
			QuotaWindow{Present: true, RemainingPercent: 0},
		},
		{
			"half spent",
			QuotaWindowFields{Reported: true, UsedPercent: ptr(40)}, now,
			QuotaWindow{Present: true, RemainingPercent: 60},
		},
		{
			"59 seconds round up to one minute",
			QuotaWindowFields{Reported: true, LimitWindowSeconds: ptr(59)}, now,
			QuotaWindow{Present: true, RemainingPercent: 100, WindowMinutes: 1},
		},
		{
			"a whole hour reads as 60 minutes",
			QuotaWindowFields{Reported: true, LimitWindowSeconds: ptr(3600)}, now,
			QuotaWindow{Present: true, RemainingPercent: 100, WindowMinutes: 60},
		},
		{
			"3661 seconds round up to 62 minutes",
			QuotaWindowFields{Reported: true, LimitWindowSeconds: ptr(3661)}, now,
			QuotaWindow{Present: true, RemainingPercent: 100, WindowMinutes: 62},
		},
		{
			"a non-positive length is an absent length",
			QuotaWindowFields{Reported: true, LimitWindowSeconds: ptr(0)}, now,
			QuotaWindow{Present: true, RemainingPercent: 100},
		},
		{
			"the absolute epoch wins over the offset",
			QuotaWindowFields{Reported: true, ResetAt: ptr(1_750_000_500), ResetAfterSeconds: ptr(9_999)}, now,
			QuotaWindow{Present: true, RemainingPercent: 100, ResetAt: time.Unix(1_750_000_500, 0).UTC()},
		},
		{
			"without an epoch the offset counts from now",
			QuotaWindowFields{Reported: true, ResetAfterSeconds: ptr(300)}, now,
			QuotaWindow{Present: true, RemainingPercent: 100, ResetAt: now.Add(300 * time.Second)},
		},
		{
			"a non-positive epoch falls back to the offset",
			QuotaWindowFields{Reported: true, ResetAt: ptr(0), ResetAfterSeconds: ptr(120)}, now,
			QuotaWindow{Present: true, RemainingPercent: 100, ResetAt: now.Add(120 * time.Second)},
		},
		{
			"a negative offset is an absent deadline",
			QuotaWindowFields{Reported: true, ResetAfterSeconds: ptr(-1)}, now,
			QuotaWindow{Present: true, RemainingPercent: 100},
		},
		{
			"every reported field together",
			QuotaWindowFields{Reported: true, UsedPercent: ptr(25), LimitWindowSeconds: ptr(300), ResetAt: ptr(1_750_001_200)},
			now,
			QuotaWindow{Present: true, RemainingPercent: 75, WindowMinutes: 5, ResetAt: time.Unix(1_750_001_200, 0).UTC()},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := NormalizeQuotaWindow(testCase.fields, testCase.now)
			if !reflect.DeepEqual(got, testCase.want) {
				t.Fatalf("NormalizeQuotaWindow(%+v) = %+v, want %+v", testCase.fields, got, testCase.want)
			}
		})
	}
}
