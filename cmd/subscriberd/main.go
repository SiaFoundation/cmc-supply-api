package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/modules/consensus"
	"go.sia.tech/siad/modules/gateway"
	"go.sia.tech/siad/modules/transactionpool"
	"go.uber.org/zap"
)

var (
	dir       string
	bootstrap bool

	logStdout bool
	logLevel  string

	gatewayAddr = ":9981"
)

func init() {
	flag.StringVar(&dir, "dir", "", "directory to store data")
	flag.BoolVar(&bootstrap, "bootstrap", true, "bootstrap the network")
	flag.BoolVar(&logStdout, "log.stdout", true, "log to stdout")
	flag.StringVar(&logLevel, "log.level", "info", "log level")
	flag.Parse()
}

// syncLastChange writes the last change id to disk.
func syncLastChange(cc modules.ConsensusChangeID) error {
	// create a temp file
	f, err := os.Create(filepath.Join(dir, "lastchange.tmp"))
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	defer f.Close()

	// write the change id to the temp file and rename it
	if _, err := f.Write(cc[:]); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	} else if err := f.Sync(); err != nil {
		return fmt.Errorf("failed to sync temp file: %w", err)
	} else if err := f.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	} else if err := os.Rename(filepath.Join(dir, "lastchange.tmp"), filepath.Join(dir, "lastchange")); err != nil {
		return fmt.Errorf("failed to rename temp file: %w", err)
	}
	return nil
}

// readLastChange reads the last change id from disk.
func readLastChange() (cc modules.ConsensusChangeID, err error) {
	// if the lastchange file doesn't exist, return an empty change id
	if _, err := os.Stat(filepath.Join(dir, "lastchange")); os.IsNotExist(err) {
		return modules.ConsensusChangeID{}, nil
	} else if err != nil {
		return modules.ConsensusChangeID{}, fmt.Errorf("failed to stat lastchange: %w", err)
	}

	// read the lastchange file
	f, err := os.Open(filepath.Join(dir, "lastchange"))
	if err != nil {
		return modules.ConsensusChangeID{}, fmt.Errorf("failed to open lastchange: %w", err)
	}
	defer f.Close()
	if _, err := f.Read(cc[:]); err != nil {
		return modules.ConsensusChangeID{}, fmt.Errorf("failed to read lastchange: %w", err)
	}
	return
}

func main() {
	if err := os.MkdirAll(dir, 0700); err != nil {
		panic(err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	cfg := zap.NewProductionConfig()
	cfg.OutputPaths = []string{filepath.Join(dir, "log.log")}
	if logStdout {
		cfg.OutputPaths = append(cfg.OutputPaths, "stdout")
	}
	switch logLevel {
	case "debug":
		cfg.Level = zap.NewAtomicLevelAt(zap.DebugLevel)
	case "info":
		cfg.Level = zap.NewAtomicLevelAt(zap.InfoLevel)
	case "warn":
		cfg.Level = zap.NewAtomicLevelAt(zap.WarnLevel)
	default:
		cfg.Level = zap.NewAtomicLevelAt(zap.InfoLevel)
	}
	log, err := cfg.Build()
	if err != nil {
		panic(err)
	}

	// start the gateway
	g, err := gateway.New(gatewayAddr, bootstrap, filepath.Join(dir, "gateway"))
	if err != nil {
		log.Panic("failed to create gateway", zap.Error(err))
	}
	defer g.Close()

	// start the consensus set
	cs, errCh := consensus.New(g, bootstrap, filepath.Join(dir, "consensus"))
	select {
	case err := <-errCh:
		if err != nil {
			log.Panic("failed to create consensus", zap.Error(err))
		}
	default:
		go func() {
			if err := <-errCh; err != nil && !strings.Contains(err.Error(), "ThreadGroup already stopped") {
				log.Panic("failed to initialize consensus", zap.Error(err))
			}
		}()
	}
	defer cs.Close()

	// start the transaction pool
	tp, err := transactionpool.New(cs, g, filepath.Join(dir, "tpool"))
	if err != nil {
		log.Panic("failed to create transaction pool", zap.Error(err))
	}
	defer tp.Close()

	lastProcessedChange, err := readLastChange()
	if err != nil {
		log.Panic("failed to read last consensus change", zap.Error(err))
	}

	// create a subscriber
	subscriber := &siaSubscriber{log: log.Named("subscriber")}
	go func() {
		// subscribe to the consensus set
		// done in a goroutine to prevent blocking the main thread
		log.Info("subscribing to consensus set", zap.Stringer("last change", lastProcessedChange))
		if err := cs.ConsensusSetSubscribe(subscriber, lastProcessedChange, ctx.Done()); err != nil {
			log.Panic("failed to subscribe to consensus set", zap.Error(err))
		}
	}()

	// subscribe to the transaction pool
	tp.TransactionPoolSubscribe(subscriber)

	// wait for the context to be canceled
	<-ctx.Done()
	log.Info("shutting down")
}
