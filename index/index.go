package index

import (
	"bytes"
	"context"
	"errors"
	"time"

	"go.sia.tech/core/types"
	"go.sia.tech/walletd/api"
	"go.uber.org/zap"
)

type State struct {
	Index             types.ChainIndex
	CirculatingSupply types.Currency
	TotalSupply       types.Currency
	BurnedSupply      types.Currency
}

type AddressDelta struct {
	Address  types.Address
	Incoming types.Currency
	Outgoing types.Currency
}

type Store interface {
	State() (State, error)

	UpdateState(state State, deltas []AddressDelta, newFoundationAddresses []types.Address) error
}

// UpdateConsensusState indexes consensus updates from the walletd API.
func UpdateConsensusState(ctx context.Context, store Store, client *api.Client, log *zap.Logger) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(15 * time.Second):
		}

		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			state, err := store.State()
			if err != nil {
				log.Fatal("failed to get last index", zap.Error(err))
			}

			reverted, applied, err := client.ConsensusUpdates(state.Index, 100)
			if err != nil {
				log.Fatal("failed to get consensus updates", zap.Error(err))
			} else if len(reverted) == 0 && len(applied) == 0 {
				continue
			}

			addressDeltas := make(map[types.Address]*AddressDelta)
			incrementAddressDelta := func(addr types.Address, incoming, outgoing types.Currency) {
				if _, ok := addressDeltas[addr]; !ok {
					addressDeltas[addr] = &AddressDelta{
						Address: addr,
					}
				}
				addressDeltas[addr].Incoming = addressDeltas[addr].Incoming.Add(incoming)
				addressDeltas[addr].Outgoing = addressDeltas[addr].Outgoing.Add(outgoing)
			}
			for _, cru := range reverted {
				// cru.State.Index is the parent of the reverted block
				// calculate the index of the block that was reverted
				revertedIndex := types.ChainIndex{
					ID:     cru.Block.ID(),
					Height: cru.State.Index.Height + 1,
				}
				log := log.With(zap.Stringer("blockID", revertedIndex.ID), zap.Uint64("height", revertedIndex.Height))

				// state is already the post-reverted state
				state.TotalSupply = state.TotalSupply.Sub(cru.State.BlockReward())
				sco, ok := cru.State.FoundationSubsidy()
				if ok {
					state.TotalSupply = state.TotalSupply.Sub(sco.Value)
				}

				cru.ForEachSiacoinElement(func(sce types.SiacoinElement, created, spent bool) {
					switch {
					case created && spent:
						return
					case sce.SiacoinOutput.Address == types.VoidAddress:
						// void outputs can't be spent, revert the burn
						state.TotalSupply = state.TotalSupply.Add(sce.SiacoinOutput.Value)
						state.BurnedSupply = state.BurnedSupply.Sub(sce.SiacoinOutput.Value)
					case created:
						incrementAddressDelta(sce.SiacoinOutput.Address, types.ZeroCurrency, sce.SiacoinOutput.Value)
						state.CirculatingSupply = state.CirculatingSupply.Sub(sce.SiacoinOutput.Value)
					case spent:
						incrementAddressDelta(sce.SiacoinOutput.Address, sce.SiacoinOutput.Value, types.ZeroCurrency)
						state.CirculatingSupply = state.CirculatingSupply.Add(sce.SiacoinOutput.Value)
					}
				})

				cru.ForEachV2FileContractElement(func(fce types.V2FileContractElement, created bool, rev *types.V2FileContractElement, res types.V2FileContractResolutionType) {
					if res == nil {
						return
					}

					// expiration is the only type of resolution that uses the missed host value
					_, ok := res.(*types.V2FileContractExpiration)
					if !ok {
						return
					}
					// v2 contracts don't use the void address to burn funds
					burn, ok := fce.V2FileContract.HostOutput.Value.SubWithUnderflow(fce.V2FileContract.MissedHostValue)
					if !ok {
						return
					}
					state.BurnedSupply = state.BurnedSupply.Sub(burn)
					state.TotalSupply = state.TotalSupply.Add(burn)
				})

				log.Debug("reverted index", zap.Stringer("total", state.TotalSupply), zap.Stringer("circulating", state.CirculatingSupply), zap.Stringer("burned", state.BurnedSupply))
				state.Index = cru.State.Index
			}

			var newFoundationAddresses []types.Address
			for _, cau := range applied {
				index := cau.State.Index
				log := log.With(zap.Stringer("blockID", index.ID), zap.Uint64("height", index.Height))

				if index.Height == 0 {
					for _, txn := range cau.Block.Transactions {
						for _, sco := range txn.SiacoinOutputs {
							state.TotalSupply = state.TotalSupply.Add(sco.Value)
						}
					}
					if cau.State.FoundationManagementAddress == types.VoidAddress {
						log.Panic("expected initial foundation address to be set")
					}
					newFoundationAddresses = append(newFoundationAddresses, cau.State.FoundationManagementAddress)
				} else {
					// cau.State is post-apply, need to get the pre-apply state to avoid an off-by-one
					parentState := cau.State
					parentState.Index.Height--
					state.TotalSupply = state.TotalSupply.Add(parentState.BlockReward())
					sco, ok := parentState.FoundationSubsidy()
					if ok {
						state.TotalSupply = state.TotalSupply.Add(sco.Value)
					}
				}

				cau.ForEachSiacoinElement(func(sce types.SiacoinElement, created, spent bool) {
					switch {
					case created && spent:
						return
					case sce.SiacoinOutput.Address == types.VoidAddress:
						// void outputs can't be spent, add the burn
						state.BurnedSupply = state.BurnedSupply.Add(sce.SiacoinOutput.Value)
						state.TotalSupply = state.TotalSupply.Sub(sce.SiacoinOutput.Value)
					case created:
						incrementAddressDelta(sce.SiacoinOutput.Address, sce.SiacoinOutput.Value, types.ZeroCurrency)
						state.CirculatingSupply = state.CirculatingSupply.Add(sce.SiacoinOutput.Value)
					case spent:
						incrementAddressDelta(sce.SiacoinOutput.Address, types.ZeroCurrency, sce.SiacoinOutput.Value)
						state.CirculatingSupply = state.CirculatingSupply.Sub(sce.SiacoinOutput.Value)
					}
				})

				cau.ForEachV2FileContractElement(func(fce types.V2FileContractElement, created bool, rev *types.V2FileContractElement, res types.V2FileContractResolutionType) {
					if res == nil {
						return
					}

					// expiration is the only type of resolution that uses the missed host value
					_, ok := res.(*types.V2FileContractExpiration)
					if !ok {
						return
					}
					// v2 contracts don't use the void address to burn funds
					burn, ok := fce.V2FileContract.HostOutput.Value.SubWithUnderflow(fce.V2FileContract.MissedHostValue)
					if !ok {
						return
					}
					state.BurnedSupply = state.BurnedSupply.Add(burn)
					state.TotalSupply = state.TotalSupply.Sub(burn)
				})

				for _, txn := range cau.Block.Transactions {
					for _, arb := range txn.ArbitraryData {
						if !bytes.HasPrefix(arb, types.SpecifierFoundation[:]) {
							continue
						}
						var update types.FoundationAddressUpdate
						d := types.NewBufDecoder(arb[len(types.SpecifierFoundation):])
						if update.DecodeFrom(d); d.Err() != nil {
							return errors.New("transaction contains an improperly-encoded FoundationAddressUpdate")
						}
						newFoundationAddresses = append(newFoundationAddresses, update.NewPrimary)
					}
				}
				state.Index = cau.State.Index
				log.Debug("applied index", zap.Stringer("total", state.TotalSupply), zap.Stringer("circulating", state.CirculatingSupply), zap.Stringer("burned", state.BurnedSupply))
			}

			if state.TotalSupply.Cmp(state.CirculatingSupply) < 0 {
				panic("total supply < circulating supply")
			}

			deltas := make([]AddressDelta, len(addressDeltas))
			for _, d := range addressDeltas {
				deltas = append(deltas, *d)
			}
			if err := store.UpdateState(state, deltas, newFoundationAddresses); err != nil {
				log.Fatal("failed to update state", zap.Error(err))
			}
		}
	}
}
