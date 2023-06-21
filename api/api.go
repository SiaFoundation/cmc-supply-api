package api

import (
	"net/http"
	"strings"

	"github.com/shopspring/decimal"
	"go.sia.tech/cmc-supply-api/stats"
	"go.sia.tech/jape"
	"go.uber.org/zap"
)

const (
	supplyTypeTotal       = "total"
	supplyTypeCirculating = "circulating"
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

func (a *api) handleGETSupply(c jape.Context) {
	var supplyType string
	if err := c.DecodeParam("type", &supplyType); err != nil {
		return
	}

	stats := a.sp.Stats()

	switch strings.ToLower(supplyType) {
	case supplyTypeTotal:
		c.Encode(decimal.NewFromBigInt(stats.TotalSupply.Big(), -24).InexactFloat64()) // 1 SC = 10^24 H
	case supplyTypeCirculating:
		c.Encode(decimal.NewFromBigInt(stats.CirculatingSupply.Big(), -24).InexactFloat64()) // 1 SC = 10^24 H
	}
}

// NewServer returns an http.Handler that serves the API.
func NewServer(sp StatProvider, log *zap.Logger) http.Handler {
	a := &api{
		log: log,
		sp:  sp,
	}

	return jape.Mux(map[string]jape.Handler{
		"GET /stats/supply/:type": a.handleGETSupply,
	})
}
