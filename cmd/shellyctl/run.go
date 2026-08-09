package main

import "fmt"

var version = "dev"

// run dispatches the CLI. UI entry points are wired in as their packages
// land; unknown commands print usage.
func run(args []string) error {
	if len(args) == 0 {
		fmt.Print(usage())
		return nil
	}
	switch args[0] {
	case "version":
		fmt.Println("shellyctl", version)
		return nil
	case "help", "-h", "--help":
		fmt.Print(usage())
		return nil
	default:
		return fmt.Errorf("unknown command %q\n%s", args[0], usage())
	}
}
