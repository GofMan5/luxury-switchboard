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

// sample streams events evenly from opened to last, each carrying the given
// reasoning and answer text, the reasoning ones first.
func sample(sent, opened, answer, last time.Duration, reasoningEvents, answerEvents int, reasoningBytes, answerBytes int64) PaceSample {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	at := func(offset time.Duration) time.Time { return base.Add(offset) }
	result := PaceSample{
		Sent: at(sent), Opened: at(opened), Last: at(last),
		Events: reasoningEvents + answerEvents, AnswerEvents: answerEvents, ReasoningEvents: reasoningEvents,
		StreamedBytes: reasoningBytes + answerBytes, ReasoningBytes: reasoningBytes,
	}
	if answerEvents > 0 {
		result.Answer = at(answer)
	}
	return result
}

func requested(value PaceSample, asked bool) PaceSample {
	value.ReasoningRequested = asked
	return value
}

func TestPaceSampleMeasuresTheWindowItsTokensWereGeneratedIn(t *testing.T) {
	const second = time.Second
	cases := map[string]struct {
		sample            PaceSample
		output, reasoning int64
		want              float64
	}{
		// 401 tokens over 4s: 100 tok/s, the 1s wait before them excluded.
		"plain answer": {sample(0, second, second, 5*second, 0, 80, 0, 1600), 401, 0, 100},
		// Digits run under two bytes a token; an answer that never reasoned
		// hides nothing however dense it is.
		"dense answer, short window": {sample(0, 3*second, 3*second, 4*second, 0, 80, 0, 150), 101, 0, 100},
		// Reasoning streamed in full: thinking and answer share the window.
		"reasoning shown in full": {sample(0, second, 3*second, 5*second, 40, 40, 1600, 800), 401, 300, 100},
		// 300 reasoning tokens hidden behind a 30-byte summary: the answer's
		// 101 tokens over its own second, not 401 over the summary's.
		"reasoning summarized, reported": {sample(0, 4*second, 4500*time.Millisecond, 5500*time.Millisecond, 3, 40, 30, 400), 401, 300, 100},
		// No reasoning text at all: the first token is the answer's.
		"reasoning hidden, reported": {sample(0, 4*second, 4*second, 5*second, 0, 40, 0, 400), 401, 300, 100},
		// Unreported, summarized, the window shorter than the wait that may
		// hold the hidden tokens: no honest rate.
		"reasoning summarized, unreported, short window": {sample(0, 6*second, 7*second, 8*second, 3, 40, 30, 400), 473, 0, 0},
		// Unreported, but the window outlasts the wait: within double.
		"reasoning summarized, unreported, long window": {sample(0, 2*second, 3*second, 12*second, 3, 200, 30, 2000), 1001, 0, 100},
		"reasoning summarized, unreported, no send time": {func() PaceSample {
			value := sample(0, 2*second, 3*second, 12*second, 3, 200, 30, 2000)
			value.Sent = time.Time{}
			return value
		}(), 1001, 0, 0},
		// Reasoning asked for, then hidden entirely: no reasoning text, no
		// reported size, the answer burst after a long silent wait. The
		// billed output may hold the reasoning, so the burst's window alone
		// cannot time it.
		"reasoning requested, hidden, unreported, short window": {requested(sample(0, 6*second, 6*second, 7*second, 0, 40, 0, 400), true), 473, 0, 0},
		// Every billed token streamed as text: nothing was hidden.
		"reasoning requested, none hidden, short window":       {requested(sample(0, 6*second, 6*second, 7*second, 0, 40, 0, 1600), true), 101, 0, 100},
		"reasoning requested, hidden, unreported, long window": {requested(sample(0, 2*second, 2*second, 12*second, 0, 200, 0, 2000), true), 1001, 0, 100},
		"reasoning requested, hidden, unreported, no send time": {func() PaceSample {
			value := requested(sample(0, 2*second, 2*second, 12*second, 0, 200, 0, 2000), true)
			value.Sent = time.Time{}
			return value
		}(), 1001, 0, 0},
		// Reasoning asked for and shown in full, or its size reported: the
		// cases above already measure it.
		"reasoning requested, shown in full":    {requested(sample(0, 6*second, 7*second, 8*second, 40, 40, 1600, 800), true), 201, 0, 100},
		"reasoning requested, hidden, reported": {requested(sample(0, 4*second, 4*second, 5*second, 0, 40, 0, 400), true), 401, 300, 100},
		// Not asked for: a long prefill before a fast answer hides nothing.
		"reasoning not requested, short window": {sample(0, 6*second, 6*second, 7*second, 0, 40, 0, 100), 101, 0, 100},
		// Reasoning reported, no answer after it: nothing left to measure.
		"reasoning only, hidden": {sample(0, second, 0, 5*second, 20, 0, 40, 0), 401, 401, 0},
		"no token":               {PaceSample{}, 401, 0, 0},
	}
	for name, test := range cases {
		value := test.sample
		value.OutputTokens, value.ReasoningTokens = test.output, test.reasoning
		if got := value.DecodeRate(); math.Abs(got-test.want) > 1e-9 {
			t.Errorf("%s: decode rate = %v, want %v", name, got, test.want)
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
