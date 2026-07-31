package userenv

import "testing"

func TestProcessEnvironmentTakesPrecedence(t *testing.T) {
	t.Setenv("SWITCHBOARD_ENV_TEST", " process-value ")
	if value := Get("SWITCHBOARD_ENV_TEST"); value != "process-value" {
		t.Fatalf("unexpected environment value %q", value)
	}
}
