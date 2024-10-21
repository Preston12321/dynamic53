package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/Preston12321/dynamic53"
)

func main() {
	configPath := flag.String("config", "", "File path of the config")
	flag.Parse()

	if *configPath == "" {
		fmt.Println("no configuration file specified")
	}

	configFile, err := os.Open(*configPath)
	if err != nil {
		fmt.Printf("cannot open configuration file: %s\n", err.Error())
	}

	cfg, err := dynamic53.LoadDaemonConfig(configFile)
	configFile.Close()

	if err != nil {
		fmt.Printf("error loading configuration: %s\n", err.Error())
		os.Exit(1)
	}

	errs := []error{}
	for _, zone := range cfg.Zones {
		if zone.Id == "" {
			errs = append(errs, fmt.Errorf("missing ID on zone '%s'", zone.Name))
		}
	}
	if err = errors.Join(errs...); err != nil {
		fmt.Printf("all zones must have an ID to generate an IAM policy: %s\n", err.Error())
		os.Exit(1)
	}

	policy, err := dynamic53.GenerateIAMPolicy(*cfg)
	if err != nil {
		fmt.Printf("failed to generate IAM policy: %s\n", err.Error())
		os.Exit(1)
	}

	fmt.Println(policy)
}
