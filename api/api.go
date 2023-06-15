package api

import (
	"net/http"

	"github.com/n8maninger/sia-coinbased/stats"
	"go.sia.tech/jape"
	"go.uber.org/zap"
)

type (
	// A StatProvider provides statistics about the current state of the Sia network.
	StatProvider interface {
		Stats() stats.BlockStats
	}

	api struct {
		log *zap.Logger

		sp StatProvider
	}
)

func (a *api) handleGETStats(c jape.Context) {
	c.Encode(a.sp.Stats())
}

// NewServer returns an http.Handler that serves the API.
func NewServer(sp StatProvider, log *zap.Logger) http.Handler {
	a := &api{
		log: log,
		sp:  sp,
	}

	return jape.Mux(map[string]jape.Handler{
		"GET /stats": a.handleGETStats,
	})
}
