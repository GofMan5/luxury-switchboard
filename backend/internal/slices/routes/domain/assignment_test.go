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
