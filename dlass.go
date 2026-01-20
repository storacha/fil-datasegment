package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"code.riba.cloud/go/toolbox/cmn"
	"code.riba.cloud/go/toolbox/ufcli"
	"github.com/mattn/go-isatty"
	"golang.org/x/xerrors"

	"github.com/storacha/fil-datasegment/pkg/dlass"
)

var (
	overwriteExisting bool
	showProgress      bool
	linearWalk        bool
	manifestFilename  string
	outFilename       string
	maxConcurrency    uint
	maxRetries        uint
	segmentTimeout    uint
)

var downloadAndAssemble = &ufcli.Command{
	Usage: "Assemble FRC58 aggregate from a supplied manifest",
	Name:  "from-manifest",
	Flags: []ufcli.Flag{
		&ufcli.StringFlag{
			Name:        "manifest",
			Usage:       "Location of the aggregate manifest",
			Value:       "/dev/stdin",
			Destination: &manifestFilename,
			DefaultText: "STDIN",
		},
		&ufcli.StringFlag{
			Name:        "output",
			Usage:       "Output aggregate filename",
			Destination: &outFilename,
			DefaultText: " <aggregate_pcidv2>.car ",
		},
		&ufcli.BoolFlag{
			Name:        "overwrite",
			Usage:       "Overwrite existing output if any",
			Destination: &overwriteExisting,
		},
		&ufcli.BoolFlag{
			Name:        "in-order",
			Usage:       "Do not randomize the order of downloads - order ascending by size",
			Destination: &linearWalk,
		},
		&ufcli.UintFlag{
			Name:        "max-concurrency",
			Usage:       "How many downloads to execute in parallel",
			Value:       5,
			Destination: &maxConcurrency,
		},
		&ufcli.UintFlag{
			Name:        "max-retries",
			Usage:       "Maximum amount of retries",
			Value:       5,
			Destination: &maxRetries,
		},
		&ufcli.UintFlag{
			Name:        "segment-timeout-seconds",
			Usage:       "Maximum amount of seconds download of one segment should take",
			Value:       60 * 10, // 10 mins
			Destination: &segmentTimeout,
		},
		&ufcli.BoolFlag{
			Name:        "show-progress",
			Usage:       "Show a progress meter",
			Value:       isatty.IsTerminal(os.Stderr.Fd()),
			Destination: &showProgress,
		},
	},
	Action: func(cctx *ufcli.Context) error {

		// parse manifest
		//
		manifestFh, err := os.Open(cctx.String("manifest"))
		if err != nil {
			return cmn.WrErr(err)
		}
		defer manifestFh.Close()
		if isatty.IsTerminal(manifestFh.Fd()) {
			return xerrors.New("reading the manifest from a TTY makes little sense: check the value of your --manifest flag")
		}

		var aggManifest dlass.Agg
		if err := json.NewDecoder(manifestFh).Decode(&aggManifest); err != nil {
			return cmn.WrErr(err)
		}
		manifestFh.Close()

		if aggManifest.FRC58CommP == nil {
			return xerrors.New("for the time being input manifest must specify a `frc58_aggregate` CID")
		}

		// Build assembler config from CLI flags
		config := dlass.AssemblerConfig{
			ProcessorConfig: dlass.ProcessorConfig{
				MaxRetries:     int(maxRetries),
				RetryWaitMin:   3 * time.Second,
				RetryWaitMax:   15 * time.Second,
				SegmentTimeout: time.Duration(segmentTimeout) * time.Second,
			},
			MaxConcurrency: int(maxConcurrency),
			OverwriteFile:  overwriteExisting,
			RandomizeOrder: !linearWalk,
			OutputFilename: outFilename,
		}

		// Optional progress callback
		var progressCallback dlass.AssemblyProgressCallback
		if showProgress {
			progressCallback = func(stats dlass.AssemblyStats) {
				fmt.Fprintf(os.Stderr, "Segments total:%d existing:%d downloaded:%d / %.02fGiB\n",
					stats.TotalSegments, stats.ExistingSegments, stats.DownloadedSegments,
					float64(stats.TotalBytes)/(1<<30))
			}
		}

		// Execute assembly
		_, err = dlass.AssembleAggregate(cctx.Context, &aggManifest, config, progressCallback)
		return err
	},
}
