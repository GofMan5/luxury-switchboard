package routes

import (
	"github.com/luxuryprivate/switchboard/backend/internal/slices/publictunnel/application"
)

type Source struct{ routes application.Routes }

func NewSource(routes application.Routes) *Source { return &Source{routes: routes} }
func (source *Source) Count() int                 { return len(source.routes.List()) }
