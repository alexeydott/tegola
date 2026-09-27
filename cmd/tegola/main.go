package main

import (
	"os"

	_ "github.com/theckman/goconstraint/go1.8/gte"

	"github.com/go-spatial/tegola/cmd/tegola/cmd"
	"github.com/go-spatial/tegola/internal/log"
)

func main() {
	if err := cmd.RootCmd.Execute(); err != nil {
		log.Error(err.Error())
		os.Exit(1)
	}
}
