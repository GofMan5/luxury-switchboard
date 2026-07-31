package domain

type Config struct {
	Token           string
	RPMPerIP        int
	ContextLimitKiB int
	BrandResponse   string
}

type Route struct {
	PublicModel     string
	UpstreamModel   string
	ProviderID      string
	ContextLimitKiB int
}
