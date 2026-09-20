package main

import "fmt"

// version is overridden at build time via -ldflags "-X main.version=<tag>".
var version = "dev"

func runVersion(args []string) error {
	fmt.Println("backup-engine " + version)
	return nil
}
