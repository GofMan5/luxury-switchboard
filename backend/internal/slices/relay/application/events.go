package application

// RouteEventSink receives routing decisions the operator should see as they
// happen. The relay drives it from the failover switch and the billing
// verdict; whoever implements it writes the words.
type RouteEventSink interface {
	FailoverOccurred(fromProviderName, toProviderName, publicModel string)
	// BalanceExhausted is the provider saying the account behind this key has
	// no money: the key parks for a short re-check interval, and the operator
	// is the only one who can change that verdict.
	BalanceExhausted(providerName string)
}
