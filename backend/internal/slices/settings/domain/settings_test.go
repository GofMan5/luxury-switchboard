package domain

import "testing"

func TestDefaultsAreValidAndUnsafeValuesAreRejected(t *testing.T) {
	settings := Defaults()
	if err := settings.Validate(); err != nil {
		t.Fatal(err)
	}
	settings.MaxRequestMiB = 1024
	if settings.Validate() == nil {
		t.Fatal("unsafe request limit was accepted")
	}
}

// A settings file written before the guardrails existed has no mode in it. Reading
// that as an invalid value would fail validation and discard every setting the
// operator had configured, so the zero value must become the default instead.
// The stream timers are the same story: a file from before they existed carries
// no heartbeat or probation, and that silence must not read as an out-of-range
// choice.
func TestSettingsWrittenBeforeAFieldExistedStayValid(t *testing.T) {
	older := Defaults()
	older.GuardrailMode = ""
	older.GuardrailFindings = 0
	older.HeartbeatSeconds = 0
	older.StreamProbationMilliseconds = 0
	if older.Validate() == nil {
		t.Fatal("an unfilled mode must not pass validation on its own")
	}
	filled := older.Normalized()
	if err := filled.Validate(); err != nil {
		t.Fatalf("an older settings file could not be loaded: %v", err)
	}
	if filled.GuardrailMode != DefaultGuardrailMode || filled.GuardrailFindings != Defaults().GuardrailFindings {
		t.Fatalf("normalization did not fill the missing fields: %+v", filled)
	}
	if filled.HeartbeatSeconds != Defaults().HeartbeatSeconds || filled.StreamProbationMilliseconds != Defaults().StreamProbationMilliseconds {
		t.Fatalf("normalization did not fill the missing stream timers: %+v", filled)
	}
	// Everything the operator did configure has to survive untouched.
	older.ListenerPort = 9123
	older.MaxQueued = 555
	older.HeartbeatSeconds = 42
	older.StreamProbationMilliseconds = 750
	filled = older.Normalized()
	if filled.ListenerPort != 9123 || filled.MaxQueued != 555 {
		t.Fatalf("normalization overwrote configured values: %+v", filled)
	}
	if filled.HeartbeatSeconds != 42 || filled.StreamProbationMilliseconds != 750 {
		t.Fatalf("normalization overwrote configured stream timers: %+v", filled)
	}
}

func TestNormalizedLeavesConfiguredGuardrailValuesAlone(t *testing.T) {
	settings := Defaults()
	settings.GuardrailMode = "off"
	settings.GuardrailFindings = 50
	filled := settings.Normalized()
	if filled.GuardrailMode != "off" || filled.GuardrailFindings != 50 {
		t.Fatalf("normalization overrode a deliberate choice: %+v", filled)
	}
	if !Defaults().Normalized().Equal(Defaults()) {
		t.Fatal("normalizing the defaults changed them")
	}
}

func TestStreamTimerSettingsAreValidated(t *testing.T) {
	// The heartbeat is a comment line on an idle connection: faster than five
	// seconds buys nothing, and slower than five minutes lets intermediaries
	// forget the stream exists. The probation window trades first-byte latency
	// against a retryable start, so its ends are seconds-of-feel too.
	for _, heartbeat := range []int{5, 15, 300} {
		settings := Defaults()
		settings.HeartbeatSeconds = heartbeat
		if err := settings.Validate(); err != nil {
			t.Fatalf("heartbeat %ds was rejected: %v", heartbeat, err)
		}
	}
	for _, heartbeat := range []int{4, 301, -1} {
		settings := Defaults()
		settings.HeartbeatSeconds = heartbeat
		if settings.Validate() == nil {
			t.Fatalf("heartbeat %ds was accepted", heartbeat)
		}
	}
	for _, probation := range []int{50, 250, 2000} {
		settings := Defaults()
		settings.StreamProbationMilliseconds = probation
		if err := settings.Validate(); err != nil {
			t.Fatalf("probation %dms was rejected: %v", probation, err)
		}
	}
	for _, probation := range []int{49, 2001, -1} {
		settings := Defaults()
		settings.StreamProbationMilliseconds = probation
		if settings.Validate() == nil {
			t.Fatalf("probation %dms was accepted", probation)
		}
	}
}

func TestGuardrailSettingsAreValidated(t *testing.T) {
	for _, mode := range []string{"off", "monitor", "block"} {
		settings := Defaults()
		settings.GuardrailMode = mode
		if err := settings.Validate(); err != nil {
			t.Fatalf("mode %q was rejected: %v", mode, err)
		}
	}
	// A typo must not silently disable the guardrails.
	for _, mode := range []string{"Block", "blocking", "on", "true", " monitor"} {
		settings := Defaults()
		settings.GuardrailMode = mode
		if settings.Validate() == nil {
			t.Fatalf("mode %q was accepted", mode)
		}
	}
	for _, capacity := range []int{49, 5_001, -1} {
		settings := Defaults()
		settings.GuardrailFindings = capacity
		if settings.Validate() == nil {
			t.Fatalf("capacity %d was accepted", capacity)
		}
	}
}
