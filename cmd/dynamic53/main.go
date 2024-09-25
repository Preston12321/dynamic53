package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sync"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/Preston12321/dynamic53"
)

func main() {
	configPath := flag.String("config", "", "File path of the config")
	dryRun := flag.Bool("dryrun", false, "Don't send any API requests to AWS")
	logLevel := flag.String("loglevel", "info", "Logging level")
	flag.Parse()

	// Use the global default logger to report if we're unable to parse the
	// user-supplied log level string
	level, err := zerolog.ParseLevel(*logLevel)
	if err != nil {
		log.Fatal().Err(fmt.Errorf("unable to parse log level string: %w", err)).Send()
	}

	logger := zerolog.New(os.Stdout).With().Timestamp().Logger().Level(level)

	cfg, err := dynamic53.LoadConfig(*configPath)
	if err != nil {
		logger.Fatal().Err(fmt.Errorf("error loading configuration: %w", err)).Send()
	}

	ctx := logger.WithContext(context.Background())
	ctx, cancel := context.WithCancel(ctx)

	awsCfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		logger.Fatal().Err(fmt.Errorf("cannot load AWS configuration: %w", err)).Send()
	}

	// Request SIGINT signals be sent to a channel for handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)

	daemon := dynamic53.NewDaemon(cfg, route53.NewFromConfig(awsCfg), *dryRun)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		daemon.Start(ctx)
		wg.Done()
	}()

	// Stop the running daemon if SIGINT is sent
	<-sigChan
	cancel()
	wg.Wait()
}
