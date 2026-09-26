package application

// RouteEventSink receives routing decisions the operator should see as they
// happen. The relay drives it from the failover switch; whoever implements it
// writes the words.
type RouteEventSink interface {
	FailoverOccurred(fromProviderName, toProviderName, publicModel string)
}
