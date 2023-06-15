package main

import (
	"context"
	"errors"
	"flag"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/n8maninger/sia-coinbased/api"
	"github.com/n8maninger/sia-coinbased/stats"
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
	apiAddr     = ":9980"
)

func init() {
	flag.StringVar(&dir, "dir", "", "directory to store data")
	flag.StringVar(&gatewayAddr, "gateway", defaultGatewayAddr, "gateway address")
	flag.StringVar(&apiAddr, "api", defaultAPIAddr, "api address")
	flag.BoolVar(&bootstrap, "bootstrap", true, "bootstrap the network")
	flag.BoolVar(&logStdout, "log.stdout", true, "log to stdout")
	flag.StringVar(&logLevel, "log.level", "info", "log level")
	flag.Parse()
}

func main() {
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		panic(err)
	}

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

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	go func() {
		<-ctx.Done()
		log.Info("shutting down")
		time.Sleep(time.Minute)
		os.Exit(-1)
	}()

	apiListener, err := net.Listen("tcp", apiAddr)
	if err != nil {
		log.Panic("failed to listen on api address", zap.Error(err))
	}
	defer apiListener.Close()

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

	// create a subscriber
	sp, err := stats.NewProvider(cs, log.Named("stats"))
	if err != nil {
		log.Panic("failed to create stats provider", zap.Error(err))
	}

	api := http.Server{
		Handler:     api.NewServer(sp, log.Named("api")),
		ReadTimeout: 30 * time.Second,
	}
	defer api.Close()

	go func() {
		err := api.Serve(apiListener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Panic("failed to serve api", zap.Error(err))
		}
	}()

	// wait for the context to be canceled
	<-ctx.Done()
}
