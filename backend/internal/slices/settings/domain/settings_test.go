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
