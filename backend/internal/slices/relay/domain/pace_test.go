package domain

import (
	"math"
	"testing"
	"time"
)

func TestDecodeRateExcludesFirstTokenAndPrefill(t *testing.T) {
	// 101 tokens, the first at TTFT, the remaining 100 over one second.
	if got := DecodeRate(101, 50, time.Second); math.Abs(got-100) > 1e-9 {
		t.Fatalf("decode rate = %v, want 100", got)
	}
}

func TestDecodeRateRejectsBurstEvidence(t *testing.T) {
	cases := map[string]struct {
		tokens int64
		chunks int
		window time.Duration
	}{
		"too few tokens":       {MinPaceTokens - 1, 40, time.Second},
		"buffered replay":      {300, MinPaceChunks - 1, time.Second},
		"window too short":     {300, 40, MinPaceWindow - time.Millisecond},
		"no first token":       {300, 0, 0},
		"negative window":      {300, 40, -time.Second},
		"unreported usage":     {0, 40, time.Second},
		"single-chunk gateway": {256, 1, 2 * time.Second},
	}
	for name, test := range cases {
		if got := DecodeRate(test.tokens, test.chunks, test.window); got != 0 {
			t.Fatalf("%s: decode rate = %v, want 0", name, got)
		}
	}
}

func TestDecodeRateAcceptsGuardBoundaries(t *testing.T) {
	got := DecodeRate(MinPaceTokens, MinPaceChunks, MinPaceWindow)
	want := float64(MinPaceTokens-1) * 1000 / float64(MinPaceWindow.Milliseconds())
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("boundary decode rate = %v, want %v", got, want)
	}
}
