package dynamic53

import (
	"bytes"
	"fmt"
	"text/template"
)

// listSeparator provides a clean way to write templates that involve lists
// needing separators that can't be repeated after the final item, e.g. JSON
// lists. Given the length of the list, and the current index, listSeparator
// will either return the given separator or an empty string.
func listSeparator(length int, index int, separator string) string {
	if index == length-1 {
		return ""
	}
	return separator
}

// GenerateIAMPolicy returns a JSON string representing an identity-based AWS
// IAM policy granting the necessary permissions for a dynamic53 client to
// manage the zones and records specified in the given configuration.
func GenerateIAMPolicy(cfg DaemonConfig) (string, error) {
	err := cfg.Validate()
	if err != nil {
		return "", err
	}

	funcs := template.FuncMap{"listSeparator": listSeparator}
	tmpl, err := template.New("iam-policy").Funcs(funcs).Parse(IAM_POLICY_TEMPLATE)
	if err != nil {
		return "", fmt.Errorf("unable to parse policy template: %w", err)
	}

	var buffer bytes.Buffer
	err = tmpl.Execute(&buffer, cfg.Zones)
	if err != nil {
		return "", fmt.Errorf("unable to template policy json: %w", err)
	}

	return buffer.String(), nil
}
