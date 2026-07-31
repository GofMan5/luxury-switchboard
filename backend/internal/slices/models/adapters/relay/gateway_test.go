package relay

import "testing"

func TestExtractModelIDsAcceptsCommonCatalogShapes(t *testing.T) {
	models, err := extractModelIDs([]byte(`{"data":[{"id":"gpt-z"},{"model":"gpt-a"},{"id":"gpt-z"}]}`))
	if err != nil || len(models) != 2 || models[0] != "gpt-a" || models[1] != "gpt-z" {
		t.Fatalf("unexpected models: %v err=%v", models, err)
	}
	models, err = extractModelIDs([]byte(`{"models":["claude-b",{"name":"claude-a"}]}`))
	if err != nil || len(models) != 2 || models[0] != "claude-a" {
		t.Fatalf("unexpected alternate catalog: %v err=%v", models, err)
	}
}
