package main

import (
	"sync"

	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
	"go.uber.org/zap"
)

type siaSubscriber struct {
	log *zap.Logger

	mu        sync.Mutex
	tpoolTxns map[modules.TransactionSetID][]types.Transaction
}

// ProcessConsensusChange implements modules.ConsensusSetSubscriber.
func (ss *siaSubscriber) ProcessConsensusChange(cc modules.ConsensusChange) {
	for i, reverted := range cc.RevertedBlocks {
		_ = cc.RevertedDiffs[i]
		ss.log.Info("reverted block", zap.String("id", reverted.ID().String()))
	}
	for i, applied := range cc.AppliedBlocks {
		_ = cc.AppliedDiffs[i]
		ss.log.Info("applied block", zap.String("id", applied.ID().String()))
	}
	ss.log.Info("processed consensus change", zap.String("id", cc.ID.String()), zap.Uint64("height", uint64(cc.BlockHeight)))
	if err := syncLastChange(cc.ID); err != nil {
		ss.log.Panic("failed to sync last change", zap.Error(err))
	}
}

// ReceiveUpdatedUnconfirmedTransactions implements modules.TransactionPoolSubscriber.
func (ss *siaSubscriber) ReceiveUpdatedUnconfirmedTransactions(diff *modules.TransactionPoolDiff) {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	for _, txnsetID := range diff.RevertedTransactions {
		_, ok := ss.tpoolTxns[txnsetID]
		if !ok {
			continue
		}
		delete(ss.tpoolTxns, txnsetID)
	}

	for _, txnset := range diff.AppliedTransactions {
		ss.tpoolTxns[txnset.ID] = txnset.Transactions
	}

	var n int
	for _, txnset := range ss.tpoolTxns {
		n += len(txnset)
	}
	ss.log.Info("processed transaction pool diff", zap.Int("reverted", len(diff.RevertedTransactions)), zap.Int("applied", len(diff.AppliedTransactions)), zap.Int("transactions", n))
}
