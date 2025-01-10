package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"time"

	"github.com/shopspring/decimal"
	"go.sia.tech/core/types"
	"go.sia.tech/jape"
	"go.sia.tech/walletd/api"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Supply represents the total and circulating supply of Siacoins.
type Supply struct {
	Index             types.ChainIndex
	TotalSupply       types.Currency
	CirculatingSupply types.Currency

	// treasury assets are excluded by CMC's definition of circulating supply
	// the UTXO ID is tracked since the treasury address can change
	FoundationUTXOs map[types.SiacoinOutputID]types.Currency
}

func checkFatalError(context string, err error) {
	if err != nil {
		os.Stderr.WriteString(fmt.Sprintf("%s: %v\n", context, err))
		os.Exit(1)
	}
}

func syncState(fp string, state Supply) error {
	tempPath := fp + ".tmp"
	f, err := os.Create(tempPath)
	if err != nil {
		return fmt.Errorf("failed to create state file: %w", err)
	}
	defer f.Close()

	if err = json.NewEncoder(f).Encode(state); err != nil {
		return fmt.Errorf("failed to encode state: %w", err)
	} else if err = f.Sync(); err != nil {
		return fmt.Errorf("failed to sync state file: %w", err)
	} else if err = f.Close(); err != nil {
		return fmt.Errorf("failed to close state file: %w", err)
	} else if err = os.Rename(tempPath, fp); err != nil {
		return fmt.Errorf("failed to rename state file: %w", err)
	}
	return nil
}

func main() {
	var (
		dir                = "."
		walletdAPIAddr     = "http://localhost:9980/api"
		walletdAPIPassword = ""
		logLevel           = "info"
	)
	flag.StringVar(&dir, "dir", dir, "Directory to store the supply data")
	flag.StringVar(&walletdAPIAddr, "api", walletdAPIAddr, "Walletd API address")
	flag.StringVar(&walletdAPIPassword, "password", walletdAPIPassword, "Walletd API password")
	flag.StringVar(&logLevel, "log", logLevel, "Log level")
	flag.Parse()

	cfg := zap.NewProductionEncoderConfig()
	cfg.TimeKey = "" // prevent duplicate timestamps
	cfg.EncodeTime = zapcore.RFC3339TimeEncoder
	cfg.EncodeDuration = zapcore.StringDurationEncoder
	cfg.EncodeLevel = zapcore.CapitalColorLevelEncoder

	cfg.StacktraceKey = ""
	cfg.CallerKey = ""
	encoder := zapcore.NewConsoleEncoder(cfg)

	var level zap.AtomicLevel
	switch logLevel {
	case "debug":
		level = zap.NewAtomicLevelAt(zap.DebugLevel)
	case "info":
		level = zap.NewAtomicLevelAt(zap.InfoLevel)
	case "warn":
		level = zap.NewAtomicLevelAt(zap.WarnLevel)
	case "error":
		level = zap.NewAtomicLevelAt(zap.ErrorLevel)
	default:
		fmt.Printf("invalid log level %q", level)
		os.Exit(1)
	}

	log := zap.New(zapcore.NewCore(encoder, zapcore.Lock(os.Stdout), level))
	defer log.Sync()

	zap.RedirectStdLog(log)

	var mu sync.Mutex // protects the state
	state := Supply{
		FoundationUTXOs: make(map[types.SiacoinOutputID]types.Currency),
	}
	stateFilePath := filepath.Join(dir, "state.json")
	if _, err := os.Stat(stateFilePath); err == nil {
		log.Debug("loading state from file", zap.String("path", stateFilePath))
		f, err := os.Open(stateFilePath)
		checkFatalError("open state file", err)
		defer f.Close()
		err = json.NewDecoder(f).Decode(&state)
		checkFatalError("decode state file", err)
	} else if !errors.Is(err, os.ErrNotExist) {
		checkFatalError("stat state file", err)
	}

	wc := api.NewClient(walletdAPIAddr, walletdAPIPassword)
	_, err := wc.ConsensusTip()
	checkFatalError("failed to validate walletd credentials", err)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	var lastSync time.Time
	go func() {
		for {
			reverted, applied, err := wc.ConsensusUpdates(state.Index, 100)
			if err != nil {
				log.Fatal("failed to get consensus updates", zap.Error(err))
			} else if len(reverted) == 0 && len(applied) == 0 {
				log.Debug("no consensus updates")
				select {
				case <-ctx.Done():
					return
				case <-time.After(15 * time.Second):
					continue
				}
			}

			mu.Lock()
			for _, cru := range reverted {
				revertedBlockID := cru.Block.ID()
				revertedHeight := cru.State.Index.Height + 1
				log := log.With(zap.Stringer("blockID", revertedBlockID), zap.Uint64("height", revertedHeight))
				foundationSubsidyID := revertedBlockID.FoundationOutputID()
				cru.ForEachSiacoinElement(func(sce types.SiacoinElement, created, spent bool) {
					if created && spent {
						return
					}

					switch {
					case created && spent:
						return
					case created:
						switch {
						case sce.SiacoinOutput.Address == types.VoidAddress:
							// ignore burnt utxos
						case sce.ID == foundationSubsidyID:
							delete(state.FoundationUTXOs, foundationSubsidyID)
						default:
							state.CirculatingSupply = state.CirculatingSupply.Sub(sce.SiacoinOutput.Value)
						}
					case spent:
						if sce.ID == foundationSubsidyID {
							state.FoundationUTXOs[foundationSubsidyID] = sce.SiacoinOutput.Value
							return
						}
						state.CirculatingSupply = state.CirculatingSupply.Add(sce.SiacoinOutput.Value)
					}
				})

				// subtract the reverted child block's state
				state.TotalSupply = state.TotalSupply.Sub(cru.State.BlockReward())
				sco, exists := cru.State.FoundationSubsidy()
				if exists {
					state.TotalSupply = state.TotalSupply.Add(sco.Value)
				}
				state.Index = cru.State.Index
				log.Debug("reverted consensus update", zap.Stringer("totalSupply", state.TotalSupply), zap.Stringer("circulatingSupply", state.CirculatingSupply))
			}

			for _, cau := range applied {
				log := log.With(zap.Stringer("blockID", cau.State.Index.ID), zap.Uint64("height", cau.State.Index.Height))
				foundationSubsidyID := cau.State.Index.ID.FoundationOutputID()
				cau.ForEachSiacoinElement(func(sce types.SiacoinElement, created, spent bool) {
					if cau.State.Index.Height == 0 {
						state.TotalSupply = state.TotalSupply.Add(sce.SiacoinOutput.Value) // if it exists, add the genesis air drop to the total supply too. Mainnet did not have an airdrop, but Zen and Anagami testnets did.
					}

					switch {
					case created && spent:
						return
					case created:
						switch {
						case sce.SiacoinOutput.Address == types.VoidAddress:
							log.Debug("burnt coins", zap.Stringer("value", sce.SiacoinOutput.Value))
							return
						case sce.ID == foundationSubsidyID:
							state.FoundationUTXOs[sce.ID] = sce.SiacoinOutput.Value
							log.Debug("foundation UTXO", zap.Stringer("id", sce.ID), zap.Stringer("value", sce.SiacoinOutput.Value))
						default:
							state.CirculatingSupply = state.CirculatingSupply.Add(sce.SiacoinOutput.Value)
						}
					case spent:
						if _, exists := state.FoundationUTXOs[sce.ID]; exists {
							delete(state.FoundationUTXOs, sce.ID)
							return
						}
						state.CirculatingSupply = state.CirculatingSupply.Sub(sce.SiacoinOutput.Value)
					}
				})

				if cau.State.Index.Height > 0 {
					// add this block's state, not the child's
					parentState := cau.State
					parentState.Index.Height--
					parentState.Index.ID = cau.Block.ParentID
					sco, exists := parentState.FoundationSubsidy()
					if exists {
						state.TotalSupply = state.TotalSupply.Add(sco.Value)
					}
					state.TotalSupply = state.TotalSupply.Add(parentState.BlockReward())
				}
				state.Index = cau.State.Index
				if state.TotalSupply.Cmp(state.CirculatingSupply) < 0 {
					log.Fatal("total supply less than circulating supply", zap.Stringer("totalSupply", state.TotalSupply), zap.Stringer("circulatingSupply", state.CirculatingSupply))
				}
				log.Debug("applied consensus update", zap.Stringer("totalSupply", state.TotalSupply), zap.Stringer("circulatingSupply", state.CirculatingSupply))
			}

			if time.Since(lastSync) > 5*time.Minute {
				if err := syncState(stateFilePath, state); err != nil {
					log.Error("failed to sync state", zap.Error(err))
				}
				lastSync = time.Now()
			}

			log.Info("processed consensus updates", zap.Stringer("blockID", state.Index.ID), zap.Uint64("height", state.Index.Height), zap.Stringer("totalSupply", state.TotalSupply), zap.Stringer("circulatingSupply", state.CirculatingSupply))
			mu.Unlock()
		}
	}()

	l, err := net.Listen("tcp", ":8080")
	checkFatalError("failed to listen on :8080", err)
	defer l.Close()

	jape.Mux(map[string]jape.Handler{
		"GET /tip": func(jc jape.Context) {
			mu.Lock()
			defer mu.Unlock()
			jc.Encode(state.Index)
		},
		"GET /supply/circulating": func(jc jape.Context) {
			mu.Lock()
			defer mu.Unlock()
			jc.Encode(decimal.NewFromBigInt(state.TotalSupply.Big(), -24).InexactFloat64()) // 1 SC = 10^24 H
		},
		"GET /supply/total": func(jc jape.Context) {
			mu.Lock()
			defer mu.Unlock()
			jc.Encode(decimal.NewFromBigInt(state.CirculatingSupply.Big(), -24).InexactFloat64()) // 1 SC = 10^24 H
		},
	})

	s := &http.Server{
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		Handler: jape.Mux(map[string]jape.Handler{
			"GET /tip": func(jc jape.Context) {
				mu.Lock()
				defer mu.Unlock()
				jc.Encode(state.Index)
			},
			"GET /supply/circulating": func(jc jape.Context) {
				mu.Lock()
				defer mu.Unlock()
				jc.Encode(decimal.NewFromBigInt(state.TotalSupply.Big(), -24).InexactFloat64()) // 1 SC = 10^24 H
			},
			"GET /supply/total": func(jc jape.Context) {
				mu.Lock()
				defer mu.Unlock()
				jc.Encode(decimal.NewFromBigInt(state.CirculatingSupply.Big(), -24).InexactFloat64()) // 1 SC = 10^24 H
			},
		}),
	}
	defer s.Close()

	go func() {
		if err := s.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal("failed to serve HTTP", zap.Error(err))
		}
	}()

	<-ctx.Done()
}
