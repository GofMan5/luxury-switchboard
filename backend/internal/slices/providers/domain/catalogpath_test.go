package domain

import "testing"

// The join answers where a provider's catalog lives for every reader that
// asks: the table covers the shapes real bases take, including the two a naive
// concatenation gets wrong (a /v1 base and a base that already ends in the
// catalog path).
func TestJoinCatalogPath(t *testing.T) {
	cases := []struct {
		name       string
		base       string
		modelsPath string
		want       string
	}{
		{name: "plain base and path", base: "", modelsPath: "/v1/models", want: "/v1/models"},
		{name: "v1 base does not double the prefix", base: "/v1", modelsPath: "/v1/models", want: "/v1/models"},
		{name: "deep v1 base keeps its own prefix", base: "/api/v1", modelsPath: "/v1/models", want: "/api/v1/models"},
		{name: "base is the catalog path itself", base: "/v1/models", modelsPath: "/v1/models", want: "/v1/models"},
		{name: "path without a leading slash", base: "/api", modelsPath: "models", want: "/api/models"},
		{name: "trailing slash on the base", base: "/api/", modelsPath: "/models", want: "/api/models"},
		// Unreachable in practice — validation never stores an empty models
		// path — but pinned as the concat it is, so the behavior is a decision
		// rather than a surprise.
		{name: "empty path concatenates", base: "/api", modelsPath: "", want: "/api/"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := JoinCatalogPath(testCase.base, testCase.modelsPath); got != testCase.want {
				t.Fatalf("JoinCatalogPath(%q, %q) = %q, want %q", testCase.base, testCase.modelsPath, got, testCase.want)
			}
		})
	}
}
