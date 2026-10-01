package domain

import "strings"

// JoinCatalogPath answers where a provider's model catalog lives: the provider's
// own base joined with its configured models path. A base of /v1 with a catalog
// at /v1/models must not become /v1/v1/models, and a base that IS the catalog
// path must not get it appended twice. Every reader of the catalog — discovery
// through the relay, the keypool check, the health probe — joins the same way,
// so they all ask the provider the same question.
func JoinCatalogPath(base, modelsPath string) string {
	base = strings.TrimRight(base, "/")
	if base == "" || modelsPath == base || strings.HasPrefix(modelsPath, base+"/") {
		if modelsPath == "" {
			return "/"
		}
		return modelsPath
	}
	if strings.HasSuffix(base, "/v1") && (modelsPath == "/v1" || strings.HasPrefix(modelsPath, "/v1/")) {
		return base + strings.TrimPrefix(modelsPath, "/v1")
	}
	return base + "/" + strings.TrimLeft(modelsPath, "/")
}
