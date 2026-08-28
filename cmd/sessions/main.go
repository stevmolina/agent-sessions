package main

import (
	"github.com/usuario/sessions/internal/cli"
	"os"
)

func main() { os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr)) }
