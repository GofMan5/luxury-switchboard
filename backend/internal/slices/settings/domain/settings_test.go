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
func TestSettingsWrittenBeforeAFieldExistedStayValid(t *testing.T) {
	older := Defaults()
	older.GuardrailMode = ""
	older.GuardrailFindings = 0
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
	// Everything the operator did configure has to survive untouched.
	older.ListenerPort = 9123
	older.MaxQueued = 555
	filled = older.Normalized()
	if filled.ListenerPort != 9123 || filled.MaxQueued != 555 {
		t.Fatalf("normalization overwrote configured values: %+v", filled)
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
	if Defaults().Normalized() != Defaults() {
		t.Fatal("normalizing the defaults changed them")
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

// The running process re-reads the mode on its own, so changing only the mode must
// not ask the operator to restart. Anything else still must.
func TestOnlyTheGuardrailModeAppliesWithoutARestart(t *testing.T) {
	current := Defaults()
	live := current
	live.GuardrailMode = "block"
	if current.RequiresRestart(live) {
		t.Fatal("changing the guardrail mode demanded a restart")
	}
	if current.RequiresRestart(current) {
		t.Fatal("changing nothing demanded a restart")
	}
	for _, next := range []Settings{
		func() Settings { s := current; s.ListenerPort = 9000; return s }(),
		func() Settings { s := current; s.GuardrailFindings = 1_000; return s }(),
		// A restart-worthy change alongside a live one must still be reported.
		func() Settings { s := live; s.MaxQueued = 200; return s }(),
	} {
		if !current.RequiresRestart(next) {
			t.Fatalf("a restart-worthy change was missed: %+v", next)
		}
	}
}
