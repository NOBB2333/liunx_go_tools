package cli

import (
	"errors"
	"fmt"
)

func runArchive(args []string) error {
	if len(args) == 0 {
		return errors.New("archive subcommand is required: encode or decode")
	}
	command := map[string]string{
		"encode": "base64-encode",
		"decode": "base64-decode",
	}[args[0]]
	if command == "" {
		return fmt.Errorf("unknown archive subcommand: %s", args[0])
	}
	return runBase64(append([]string{command}, args[1:]...))
}

func runDocument(args []string) error {
	if len(args) == 0 {
		return errors.New("document subcommand is required: read or extract")
	}
	switch args[0] {
	case "read":
		return runWordRead(args[1:])
	case "extract":
		return runDocExtract(args[1:])
	default:
		return fmt.Errorf("unknown document subcommand: %s", args[0])
	}
}

func runNetwork(args []string) error {
	if len(args) == 0 || args[0] != "ping" {
		return errors.New("network subcommand is required: ping")
	}
	return runPing(args[1:])
}

func runCleanup(args []string) error {
	if len(args) == 0 {
		return errors.New("cleanup subcommand is required: scan or run")
	}
	switch args[0] {
	case "scan":
		return runCleanupScan(args[1:])
	case "run":
		return runCleanupRun(args[1:])
	default:
		return fmt.Errorf("unknown cleanup subcommand: %s", args[0])
	}
}

func runMockDataCommand(args []string) error {
	if len(args) == 0 || args[0] != "generate" {
		return errors.New("mockdata subcommand is required: generate")
	}
	return runMockData(args[1:])
}

func runHealth(args []string) error {
	if len(args) == 0 || args[0] != "serve" {
		return errors.New("health subcommand is required: serve")
	}
	return runAPI(args[1:])
}
