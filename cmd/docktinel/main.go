package main

import (
	"github.com/xtrime-ru/docktinel/internal/docktinel"
	"os"
)

var version = "dev"

func main() { os.Exit(docktinel.CLI(os.Args[1:], os.Stdout, os.Stderr, version)) }
