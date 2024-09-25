package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/Preston12321/dynamic53"
)

func main() {
	configPath := flag.String("config", "", "File path of the config")
	flag.Parse()

	cfg, err := dynamic53.LoadConfig(*configPath)
	if err != nil {
		fmt.Printf("error loading configuration: %s\n", err.Error())
		os.Exit(1)
	}

	policy, err := dynamic53.GenerateIAMPolicy(*cfg)
	if err != nil {
		fmt.Printf("invalid configuration: %s\n", err.Error())
		os.Exit(1)
	}

	fmt.Println(policy)
}
