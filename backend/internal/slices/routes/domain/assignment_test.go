package domain

import "testing"

func TestAssignmentRejectsInvisibleWhitespace(t *testing.T) {
	base := Assignment{Target: TargetRelay, PublicModel: "public", UpstreamModel: "upstream", ProviderID: "provider", Enabled: true}
	for _, value := range []Assignment{
		{Target: base.Target, PublicModel: " public", UpstreamModel: base.UpstreamModel, ProviderID: base.ProviderID, Enabled: true},
		{Target: base.Target, PublicModel: base.PublicModel, UpstreamModel: "upstream ", ProviderID: base.ProviderID, Enabled: true},
		{Target: base.Target, PublicModel: base.PublicModel, UpstreamModel: base.UpstreamModel, ProviderID: " provider", Enabled: true},
	} {
		if value.Validate() == nil {
			t.Fatalf("ambiguous route was accepted: %+v", value)
		}
	}
}

func TestAssignmentValidatesAliases(t *testing.T) {
	base := Assignment{Target: TargetRelay, PublicModel: "public", UpstreamModel: "upstream", ProviderID: "provider", Enabled: true}
	if err := (Assignment{Target: base.Target, PublicModel: base.PublicModel, UpstreamModel: base.UpstreamModel, ProviderID: base.ProviderID, Aliases: []string{"claude-opus-5[1m]", "claude-opus-5"}, Enabled: true}).Validate(); err != nil {
		t.Fatalf("valid aliases were rejected: %v", err)
	}
	for _, value := range []Assignment{
		{Target: base.Target, PublicModel: base.PublicModel, UpstreamModel: base.UpstreamModel, ProviderID: base.ProviderID, Aliases: []string{""}, Enabled: true},
		{Target: base.Target, PublicModel: base.PublicModel, UpstreamModel: base.UpstreamModel, ProviderID: base.ProviderID, Aliases: []string{" public"}, Enabled: true},
		{Target: base.Target, PublicModel: base.PublicModel, UpstreamModel: base.UpstreamModel, ProviderID: base.ProviderID, Aliases: []string{base.PublicModel}, Enabled: true},
	} {
		if value.Validate() == nil {
			t.Fatalf("invalid aliases were accepted: %+v", value)
		}
	}
	tooMany := make([]string, 17)
	for index := range tooMany {
		tooMany[index] = "alias" + string(rune('0'+index))
	}
	if err := (Assignment{Target: base.Target, PublicModel: base.PublicModel, UpstreamModel: base.UpstreamModel, ProviderID: base.ProviderID, Aliases: tooMany, Enabled: true}).Validate(); err == nil {
		t.Fatal("an oversized alias list was accepted")
	}
}
