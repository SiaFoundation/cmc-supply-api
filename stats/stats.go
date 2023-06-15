package stats

import (
	"bytes"
	"sync"

	"gitlab.com/NebulousLabs/encoding"
	"go.sia.tech/core/types"
	"go.sia.tech/siad/modules"
	stypes "go.sia.tech/siad/types"
	"go.uber.org/zap"
)

type (
	// BlockStats aggregates stats as of a given block.
	BlockStats struct {
		Index               types.ChainIndex `json:"index"`
		TotalSupply         types.Currency   `json:"totalSupply"`
		CirculatingSupply   types.Currency   `json:"circulatingSupply"`
		SiafundPool         types.Currency   `json:"siafundPool"`
		ActiveContractCount uint64           `json:"activeContractCount"`
	}
)

type (
	// A ConsensusSubscriber subscribes to consensus changes.
	ConsensusSubscriber interface {
		ConsensusSetSubscribe(modules.ConsensusSetSubscriber, modules.ConsensusChangeID, <-chan struct{}) error
	}

	// A Provider indexes stats on the current state of the Sia network.
	Provider struct {
		log *zap.Logger

		mu    sync.Mutex
		stats BlockStats
	}
)

// ProcessConsensusChange implements modules.ConsensusSetSubscriber.
func (p *Provider) ProcessConsensusChange(cc modules.ConsensusChange) {
	log := p.log.Named("consensusChange").With(zap.Uint64("height", uint64(cc.BlockHeight)), zap.Stringer("changeID", cc.ID))
	p.mu.Lock()
	defer p.mu.Unlock()

	// calculate the circulating supply
	for _, sd := range cc.SiacoinOutputDiffs {
		address := types.Address(sd.SiacoinOutput.UnlockHash)
		// ignore void outputs
		if address == types.VoidAddress {
			continue
		}

		var value types.Currency
		convertToCore(sd.SiacoinOutput.Value, &value)
		switch sd.Direction {
		case modules.DiffApply:
			value, overflow := p.stats.CirculatingSupply.AddWithOverflow(value)
			if overflow {
				log.Panic("circulating supply overflowed", zap.Stringer("outputID", sd.ID), zap.String("value", value.ExactString()), zap.String("circulatingSupply", p.stats.CirculatingSupply.ExactString()))
			}
			p.stats.CirculatingSupply = value
		case modules.DiffRevert:
			value, underflow := p.stats.CirculatingSupply.SubWithUnderflow(value)
			if underflow {
				log.Panic("circulating supply underflowed", zap.Stringer("outputID", sd.ID), zap.String("value", value.ExactString()), zap.String("circulatingSupply", p.stats.CirculatingSupply.ExactString()))
			}
			p.stats.CirculatingSupply = value
		default:
			log.Panic("unrecognized diff direction")
		}
	}

	// calculate the active contract count
	for _, fd := range cc.FileContractDiffs {
		switch fd.Direction {
		case modules.DiffApply:
			p.stats.ActiveContractCount++
		case modules.DiffRevert:
			p.stats.ActiveContractCount--
		default:
			log.Panic("unrecognized diff direction")
		}
	}

	// calculate the siafund pool
	for _, sd := range cc.SiafundPoolDiffs {
		switch sd.Direction {
		case modules.DiffApply:
			convertToCore(sd.Adjusted, &p.stats.SiafundPool)
		case modules.DiffRevert:
			convertToCore(sd.Previous, &p.stats.SiafundPool)
		}
	}

	// set the chain index and total supply
	p.stats.Index = types.ChainIndex{
		ID:     types.BlockID(cc.AppliedBlocks[len(cc.AppliedBlocks)-1].ID()),
		Height: uint64(cc.BlockHeight),
	}
	convertToCore(stypes.CalculateNumSiacoins(cc.BlockHeight), &p.stats.TotalSupply)
	log.Info("processed consensus change")
}

func (p *Provider) Stats() BlockStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stats
}

func convertToCore(siad encoding.SiaMarshaler, core types.DecoderFrom) {
	var buf bytes.Buffer
	siad.MarshalSia(&buf)
	d := types.NewBufDecoder(buf.Bytes())
	core.DecodeFrom(d)
	if d.Err() != nil {
		panic(d.Err())
	}
}

// NewProvider creates a new Provider.
func NewProvider(cs ConsensusSubscriber, log *zap.Logger) (*Provider, error) {
	p := &Provider{
		log: log,
	}
	go func() {
		if err := cs.ConsensusSetSubscribe(p, modules.ConsensusChangeBeginning, nil); err != nil {
			log.Panic("failed to subscribe to consensus set", zap.Error(err))
		}
	}()
	return p, nil
}
