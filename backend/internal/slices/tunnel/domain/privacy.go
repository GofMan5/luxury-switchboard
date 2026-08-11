package domain

type PrivacyHeader struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`
}

type PrivacyTLS struct {
	Version              string `json:"version"`
	CipherSuite          string `json:"cipherSuite"`
	ServerName           string `json:"serverName"`
	CertificateSubject   string `json:"certificateSubject"`
	CertificateIssuer    string `json:"certificateIssuer"`
	CertificateExpiresAt string `json:"certificateExpiresAt"`
}

type PrivacyModel struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Fields  []string `json:"fields"`
}

type PrivacyReport struct {
	CheckedAt           string          `json:"checkedAt"`
	RequestURL          string          `json:"requestUrl"`
	Status              int             `json:"status"`
	StatusText          string          `json:"statusText"`
	Protocol            string          `json:"protocol"`
	RemoteAddress       string          `json:"remoteAddress"`
	DurationMS          int64           `json:"durationMs"`
	BodyBytes           int             `json:"bodyBytes"`
	Headers             []PrivacyHeader `json:"headers"`
	TopLevelFields      []string        `json:"topLevelFields"`
	Models              []PrivacyModel  `json:"models"`
	RawBody             string          `json:"rawBody"`
	ParseError          string          `json:"parseError,omitempty"`
	CredentialReflected bool            `json:"credentialReflected"`
	TLS                 *PrivacyTLS     `json:"tls,omitempty"`
}
