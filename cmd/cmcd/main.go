package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/shopspring/decimal"
	"go.sia.tech/cmc-supply-api/index"
	"go.sia.tech/cmc-supply-api/persist/sqlite"
	"go.sia.tech/jape"
	"go.sia.tech/walletd/api"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func checkFatalError(context string, err error) {
	if err != nil {
		os.Stderr.WriteString(fmt.Sprintf("%s: %v\n", context, err))
		os.Exit(1)
	}
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

	if err := os.MkdirAll(dir, 0700); err != nil {
		log.Fatal("failed to create data directory", zap.String("dir", dir), zap.Error(err))
	}

	db, err := sqlite.OpenDatabase(filepath.Join(dir, "supply.sqlite3"), log.Named("sqlite3"))
	checkFatalError("failed to open database", err)
	defer db.Close()

	wc := api.NewClient(walletdAPIAddr, walletdAPIPassword)
	_, err = wc.ConsensusTip()
	checkFatalError("failed to validate walletd credentials", err)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	go func() {
		if err := index.UpdateConsensusState(ctx, db, wc, log.Named("index")); err != nil {
			if !errors.Is(err, context.Canceled) {
				log.Fatal("failed to index updates", zap.Error(err))
			}
		}
	}()

	l, err := net.Listen("tcp", ":8080")
	checkFatalError("failed to listen on :8080", err)
	defer l.Close()

	s := &http.Server{
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		Handler: jape.Mux(map[string]jape.Handler{
			"GET /tip": func(jc jape.Context) {
				state, err := db.State()
				if jc.Check("failed to get state", err) != nil {
					return
				}
				jc.Encode(state.Index)
			},
			"GET /supply/total": func(jc jape.Context) {
				state, err := db.State()
				if jc.Check("failed to get state", err) != nil {
					return
				}
				jc.Encode(decimal.NewFromBigInt(state.TotalSupply.Big(), -24).InexactFloat64()) // 1 SC = 10^24 H
			},
			"GET /supply/circulating": func(jc jape.Context) {
				foundationTreasury, err := db.FoundationTreasury()
				if jc.Check("failed to get foundation treasury", err) != nil {
					return
				}
				state, err := db.State()
				if jc.Check("failed to get state", err) != nil {
					return
				}
				jc.Encode(decimal.NewFromBigInt(state.CirculatingSupply.Sub(foundationTreasury).Big(), -24).InexactFloat64()) // 1 SC = 10^24 H
			},
			"GET /supply/burned": func(jc jape.Context) {
				state, err := db.State()
				if jc.Check("failed to get state", err) != nil {
					return
				}
				jc.Encode(state.BurnedSupply)
			},
			"GET /foundation/treasury": func(jc jape.Context) {
				foundationTreasury, err := db.FoundationTreasury()
				if jc.Check("failed to get foundation treasury", err) != nil {
					return
				}
				jc.Encode(decimal.NewFromBigInt(foundationTreasury.Big(), -24).InexactFloat64()) // 1 SC = 10^24 H
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
