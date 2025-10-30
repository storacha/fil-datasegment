package dlass

import (
	"context"
	"io"
	"math/rand"
	"os"
	"sort"
	"sync/atomic"

	"github.com/detailyang/go-fallocate"
	"github.com/edsrzf/mmap-go"
	"github.com/filecoin-project/go-data-segment/datasegment"
	filabi "github.com/filecoin-project/go-state-types/abi"
	"golang.org/x/sync/errgroup"
	"golang.org/x/xerrors"

	"code.riba.cloud/go/toolbox/cmn"
)

// AssemblerConfig holds configuration for the assembly process
type AssemblerConfig struct {
	ProcessorConfig ProcessorConfig // Configuration for the underlying processor
	MaxConcurrency  int              // Number of concurrent downloads (default: 5)
	OverwriteFile   bool             // Whether to overwrite existing output file
	RandomizeOrder  bool             // If false, sorts by segment size ascending (default: true)
	OutputFilename  string           // Output file path (if empty, uses aggregate CID)
}

// DefaultAssemblerConfig returns sensible defaults
func DefaultAssemblerConfig() AssemblerConfig {
	return AssemblerConfig{
		ProcessorConfig: DefaultProcessorConfig(),
		MaxConcurrency:  5,
		OverwriteFile:   false,
		RandomizeOrder:  true,
		OutputFilename:  "",
	}
}

// AssemblyStats holds statistics about the assembly process
type AssemblyStats struct {
	TotalSegments      int64
	DownloadedSegments int64
	ExistingSegments   int64
	TotalBytes         int64
}

// AssemblyProgressCallback is called periodically during assembly with current stats
type AssemblyProgressCallback func(stats AssemblyStats)

// AssembleAggregate downloads and assembles an FRC58 aggregate from a manifest
// and writes the result to a file.
//
// The function handles:
// - Validation of the aggregate manifest
// - Creation of the output file with proper allocation
// - Parallel downloading of segments with retries
// - Verification of segment integrity using CommP hashes
// - Progress reporting via optional callback
//
// Parameters:
//   - ctx: context for cancellation and timeouts
//   - manifest: the aggregate manifest to process
//   - config: assembly configuration
//   - progressCallback: optional callback for progress updates (can be nil)
//
// Returns:
//   - The path to the created file
//   - An error if assembly fails at any point
func AssembleAggregate(
	ctx context.Context,
	manifest *Agg,
	config AssemblerConfig,
	progressCallback AssemblyProgressCallback,
) (string, error) {

	if manifest == nil {
		return "", xerrors.New("manifest cannot be nil")
	}

	if manifest.FRC58CommP == nil {
		return "", xerrors.New("manifest must specify a frc58_aggregate CID")
	}

	if config.MaxConcurrency < 1 {
		config.MaxConcurrency = 5
	}

	// Validate and prep list of pieces
	toProccess := make([]PieceTask, len(manifest.PieceList))
	pis := make([]filabi.PieceInfo, len(manifest.PieceList))

	for i := range manifest.PieceList {
		ae := manifest.PieceList[i]
		if len(ae.Sources) != 1 {
			return "", xerrors.Errorf(
				"currently exactly one source is supported per piece, yet %s has %d",
				ae.PcidV2.PCidV2().String(), len(ae.Sources),
			)
		}
		pis[i] = ae.PcidV2.PieceInfo()
		toProccess[i] = PieceTask{
			Comm:        &ae.PcidV2.CommP,
			PayloadSize: int(ae.PcidV2.PayloadSize()),
			SegmentSize: int(ae.PcidV2.PieceInfo().Size.Unpadded()),
			URL:         &ae.Sources[0].URL,
		}
	}

	// Build aggregate metadata
	aggObj, err := datasegment.NewAggregate(manifest.FRC58CommP.PieceInfo().Size, pis)
	if err != nil {
		return "", cmn.WrErr(err)
	}

	// Validate that pieces aggregate correctly
	aggReifiedPcidV1, err := aggObj.PieceCID()
	if err != nil {
		return "", cmn.WrErr(err)
	}

	if aggReifiedPcidV1 != manifest.FRC58CommP.PCidV1() {
		return "", xerrors.Errorf(
			"supplied list of %d pieces does not aggregate with expected PCidV1 %s, got %s instead",
			len(manifest.PieceList),
			manifest.FRC58CommP.PCidV1(),
			aggReifiedPcidV1,
		)
	}

	// Prepare output file
	outFilename := config.OutputFilename
	if outFilename == "" {
		outFilename = manifest.FRC58CommP.PCidV2().String() + ".frc58"
	}

	oFlags := os.O_CREATE | os.O_RDWR
	if config.OverwriteFile {
		oFlags |= os.O_TRUNC
	}

	outFh, err := os.OpenFile(outFilename, oFlags, 0644)
	if err != nil {
		return "", cmn.WrErr(err)
	}
	defer func() {
		if cerr := outFh.Close(); cerr != nil {
			// Log but don't override the primary error
			_ = cerr
		}
	}()

	fhStat, err := outFh.Stat()
	if err != nil {
		return "", cmn.WrErr(err)
	}

	var maybeExisting bool
	if fhStat.Size() == int64(aggObj.DealSize.Unpadded()) {
		maybeExisting = true
	} else if err := outFh.Truncate(0); err != nil {
		return "", cmn.WrErr(err)
	}

	// Allocate file space
	if err := fallocate.Fallocate(outFh, 0, int64(aggObj.DealSize.Unpadded())); err != nil {
		return "", cmn.WrErr(err)
	}
	if err := outFh.Truncate(int64(aggObj.DealSize.Unpadded())); err != nil {
		return "", cmn.WrErr(err)
	}

	// Memory-map the file
	aggBuf, err := mmap.Map(outFh, mmap.RDWR, 0)
	if err != nil {
		return "", cmn.WrErr(err)
	}
	defer func() {
		if ferr := aggBuf.Flush(); ferr != nil {
			_ = ferr
		}
		if uerr := aggBuf.Unmap(); uerr != nil {
			_ = uerr
		}
	}()

	// Write table of contents (index)
	tocR, err := aggObj.IndexReader()
	if err != nil {
		return "", cmn.WrErr(err)
	}
	tocOffset := int(datasegment.DataSegmentIndexStartOffset(aggObj.DealSize))
	if _, err := io.ReadFull(tocR, aggBuf[tocOffset:]); err != nil {
		return "", xerrors.Errorf("failed writing ToC at offset %d: %w", tocOffset, err)
	}

	// Build task list with offsets and zero-regions
	var payloadTot int
	var lastSegmentEnd int
	for i, e := range aggObj.Index.Entries {
		toProccess[i].StartOffset = int(filabi.PaddedPieceSize(e.Offset).Unpadded())
		payloadTot += toProccess[i].PayloadSize

		if maybeExisting {
			if zeroGap := toProccess[i].StartOffset - lastSegmentEnd; zeroGap > 0 {
				toProccess = append(toProccess, PieceTask{
					StartOffset: lastSegmentEnd,
					SegmentSize: zeroGap,
				})
			}
		}
		lastSegmentEnd = toProccess[i].StartOffset + toProccess[i].SegmentSize
	}

	// Add final zero-region if needed
	if zeroGap := tocOffset - lastSegmentEnd; zeroGap > 0 {
		toProccess = append(toProccess, PieceTask{
			StartOffset: lastSegmentEnd,
			SegmentSize: zeroGap,
		})
	}

	// Shuffle or sort task order
	if config.RandomizeOrder {
		rand.Shuffle(len(toProccess), func(i, j int) {
			toProccess[i], toProccess[j] = toProccess[j], toProccess[i]
		})
	} else {
		sort.Slice(toProccess, func(i, j int) bool {
			return toProccess[i].SegmentSize < toProccess[j].SegmentSize
		})
	}

	// Create processor
	processor := NewProcessor(config.ProcessorConfig)

	// Statistics tracking
	stats := AssemblyStats{
		TotalSegments: int64(len(aggObj.Index.Entries)),
	}
	dlCount := new(int64)
	existingCount := new(int64)

	// Execute parallel downloads
	eg, egCtx := errgroup.WithContext(ctx)
	eg.SetLimit(config.MaxConcurrency)

	for _, p := range toProccess {
		if egCtx.Err() != nil {
			break
		}
		p := p
		eg.Go(func() error {
			return assembleTask(egCtx, processor, p, aggBuf[p.StartOffset:p.StartOffset+p.SegmentSize], maybeExisting, dlCount, existingCount)
		})
	}

	if err := eg.Wait(); err != nil {
		return "", err
	}

	// Update final stats
	stats.DownloadedSegments = atomic.LoadInt64(dlCount)
	stats.ExistingSegments = atomic.LoadInt64(existingCount)
	stats.TotalBytes = int64(payloadTot)

	if progressCallback != nil {
		progressCallback(stats)
	}

	return outFilename, nil
}

// assembleTask processes a single piece task
func assembleTask(
	ctx context.Context,
	processor *Processor,
	p PieceTask,
	segmentBuf []byte,
	maybeExisting bool,
	dlCount *int64,
	existingCount *int64,
) error {

	// Handle zero-fill regions
	if p.Comm == nil {
		// Just process the zero-fill task as-is
		return processor.ProcessPiece(ctx, p, segmentBuf, maybeExisting)
	}

	// Process the task
	if err := processor.ProcessPiece(ctx, p, segmentBuf, maybeExisting); err != nil {
		return err
	}

	// Track statistics
	atomic.AddInt64(dlCount, 1)
	atomic.AddInt64(existingCount, 0) // Only counted for actual downloads, not existing data

	return nil
}
