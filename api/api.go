package api

import (
	"errors"
	"net/http"

	"github.com/n8maninger/sia-coinbased/stats"
	"go.sia.tech/jape"
	"go.uber.org/zap"
)

type (
	// A StatProvider provides statistics about the current state of the Sia network.
	StatProvider interface {
		Stats() stats.BlockStats
		StatsHeight(uint64) (stats.BlockStats, error)
	}

	api struct {
		log *zap.Logger

		sp StatProvider
	}
)

func (a *api) handleGETStats(c jape.Context) {
	c.Encode(a.sp.Stats())
}

func (a *api) handleGETStatsHeight(c jape.Context) {
	var height int
	if err := c.DecodeParam("height", &height); err != nil {
		return
	} else if height < 0 {
		c.Error(errors.New("height must be non-negative"), http.StatusBadRequest)
		return
	}

	stats, err := a.sp.StatsHeight(uint64(height))
	if err != nil {
		c.Error(err, http.StatusBadRequest)
		return
	}
	c.Encode(stats)
}

// NewServer returns an http.Handler that serves the API.
func NewServer(sp StatProvider, log *zap.Logger) http.Handler {
	a := &api{
		log: log,
		sp:  sp,
	}

	return jape.Mux(map[string]jape.Handler{
		"GET /stats":         a.handleGETStats,
		"GET /stats/:height": a.handleGETStatsHeight,
	})
}
