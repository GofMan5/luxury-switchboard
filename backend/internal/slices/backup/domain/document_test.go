package domain

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Plugin secrets and plugin collections are deliberately not part of the
// backup: secrets stay write-only on the machine that saved them and plugin
// data belongs to the plugin, not the configuration. A new top-level field
// must be added here on purpose, after deciding it is safe to export.
func TestDocumentCarriesNoPluginData(t *testing.T) {
	var fields []string
	kind := reflect.TypeOf(Document{})
	for index := range kind.NumField() {
		name, _, _ := strings.Cut(kind.Field(index).Tag.Get("json"), ",")
		fields = append(fields, name)
	}
	want := []string{"version", "exportedAt", "note", "providers", "keys", "routes", "prices"}
	if !slices.Equal(fields, want) {
		t.Fatalf("the backup document fields changed to %v; plugin secrets and collections must stay out", fields)
	}
}
