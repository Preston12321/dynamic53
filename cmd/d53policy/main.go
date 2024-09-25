package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"text/template"

	_ "embed"

	"github.com/Preston12321/dynamic53"
)

//go:embed iam-policy.tmpl
var IAM_POLICY_TEMPLATE string

func main() {
	configPath := flag.String("config", "", "File path of the config")
	flag.Parse()

	cfg, err := dynamic53.LoadConfig(*configPath)
	if err != nil {
		fmt.Printf("error loading configuration: %s\n", err.Error())
		os.Exit(1)
	}

	policy, err := GenerateIAMPolicy(cfg)
	if err != nil {
		fmt.Printf("invalid configuration: %s\n", err.Error())
		os.Exit(1)
	}

	fmt.Println(policy)
}

func GenerateIAMPolicy(cfg *dynamic53.DaemonConfig) (string, error) {
	tmpl, err := template.New("iam-policy").Parse(IAM_POLICY_TEMPLATE)
	if err != nil {
		return "", fmt.Errorf("unable to parse template file: %w", err)
	}

	var buffer bytes.Buffer
	err = tmpl.Execute(&buffer, cfg.Zones)
	if err != nil {
		return "", fmt.Errorf("unable to execute template: %w", err)
	}

	return buffer.String(), nil
}
