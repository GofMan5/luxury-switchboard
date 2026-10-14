package application

import "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"

// Names and request budgets do not change what a credential authenticated to.
// Destination/auth/catalog/dialect changes do, so outstanding verdicts belong
// to the old profile rather than its durable replacement.
func sameCredentialProfile(left, right domain.Provider) bool {
	return left.BaseURL.String() == right.BaseURL.String() && left.AuthMode == right.AuthMode && left.AuthHeader == right.AuthHeader && left.Dialect == right.Dialect && left.ModelsPath == right.ModelsPath && left.Format == right.Format && left.ChatPath == right.ChatPath && left.Preset == right.Preset && left.AccountID == right.AccountID && left.Enabled == right.Enabled
}
