package main

import (
	"context"
	"fmt"
	"os"

	"code.riba.cloud/go/toolbox/ufcli"
	logging "github.com/ipfs/go-log/v2"
)

var log = logging.Logger(fmt.Sprintf("%s(%d)", "fil-datasegment", os.Getpid()))

func main() {

	logging.SetLogLevel("*", "INFO")

	(&ufcli.UFcli{
		Logger:              log,
		AllowConcurrentRuns: true,
		AppConfig: ufcli.App{
			Name:  "fil-datasegment",
			Usage: "Basic CLI app for downloading FRC58-formatted aggregates",
			Commands: []*ufcli.Command{
				downloadAndAssemble,
			},
		},
	}).RunAndExit(context.Background())
}
