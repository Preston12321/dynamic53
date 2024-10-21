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
	logLevel := flag.String("loglevel", "info", "Logging level")
	flag.Parse()

	// Use the global default logger to report if we're unable to parse the
	// user-supplied log level string
	level, err := zerolog.ParseLevel(*logLevel)
	if err != nil {
		log.Fatal().Err(fmt.Errorf("unable to parse log level string: %w", err)).Send()
	}

	logger := zerolog.New(os.Stdout).With().Timestamp().Logger().Level(level)

	if *configPath == "" {
		logger.Fatal().Err(fmt.Errorf("no configuration file specified")).Send()
	}

	configFile, err := os.Open(*configPath)
	if err != nil {
		logger.Fatal().Err(fmt.Errorf("cannot open configuration file: %w", err)).Send()
	}

	cfg, err := dynamic53.LoadDaemonConfig(configFile)
	configFile.Close()

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

	daemon := dynamic53.NewDaemon(*cfg, route53.NewFromConfig(awsCfg))

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
