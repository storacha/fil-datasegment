package dlass

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"time"

	commp "github.com/filecoin-project/go-fil-commp-hashhash"
	"github.com/hashicorp/go-retryablehttp"
	"golang.org/x/xerrors"

	"code.riba.cloud/go/toolbox/cmn"
)

// ProcessorConfig holds configuration for processing pieces
type ProcessorConfig struct {
	MaxRetries      int           // maximum number of retries for failed downloads
	RetryWaitMin    time.Duration // minimum wait time between retries
	RetryWaitMax    time.Duration // maximum wait time between retries
	SegmentTimeout  time.Duration // timeout for individual segment processing
}

// DefaultProcessorConfig returns sensible defaults
func DefaultProcessorConfig() ProcessorConfig {
	return ProcessorConfig{
		MaxRetries:     5,
		RetryWaitMin:   3 * time.Second,
		RetryWaitMax:   15 * time.Second,
		SegmentTimeout: 10 * time.Minute,
	}
}

// Processor handles downloading and assembling data segments
type Processor struct {
	config ProcessorConfig
	client *http.Client
}

// NewProcessor creates a new Processor with the given configuration
func NewProcessor(config ProcessorConfig) *Processor {
	rc := retryablehttp.NewClient()
	rc.RetryWaitMin = config.RetryWaitMin
	rc.RetryWaitMax = config.RetryWaitMax
	rc.RetryMax = config.MaxRetries
	rc.CheckRetry = retryablehttp.ErrorPropagatedRetryPolicy

	return &Processor{
		config: config,
		client: rc.StandardClient(),
	}
}

// ProcessPiece downloads and validates a single piece
// It writes the downloaded data to segmentBuf and validates the CommP hash
// If maybeExisting is true, it will first check if the existing data is valid
func (p *Processor) ProcessPiece(ctx context.Context, task PieceTask, segmentBuf []byte, maybeExisting bool) error {
	// just a zerofill
	if task.Comm == nil {
		zeroRegion(segmentBuf)
		return nil
	}

	cpHasher := new(commp.Calc)

	// preexisting data: see if what we have on disk is good
	if maybeExisting {
		cpHasher.Write(segmentBuf)
		cp, _, err := cpHasher.Digest()
		if err != nil {
			return cmn.WrErr(err)
		}
		if bytes.Equal(cp, task.Comm.Digest()) {
			return nil // what we have on disk is already good
		}

		// otherwise zero the box and proceed
		zeroRegion(segmentBuf)
	}

	// actual HTTP call
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, task.URL.String(), nil)
	if err != nil {
		return cmn.WrErr(err)
	}

	resp, err := p.client.Do(request)
	if err != nil {
		return cmn.WrErr(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return xerrors.Errorf("HTTP error code %d from %s", resp.StatusCode, task.URL)
	}

	if resp.ContentLength < 0 {
		return xerrors.Errorf("unknown Content-Length from %s", task.URL)
	}

	if _, err = io.ReadFull(
		io.TeeReader(resp.Body, cpHasher),
		segmentBuf[:int(resp.ContentLength)],
	); err != nil {
		return cmn.WrErr(err)
	}

	// pad remainder with zeros for CommP calculation
	if tooSmall := 127 - resp.ContentLength; tooSmall > 0 {
		cpHasher.Write(make([]byte, tooSmall))
	}

	cp, _, err := cpHasher.Digest()
	if err != nil {
		return cmn.WrErr(err)
	}
	if !bytes.Equal(cp, task.Comm.Digest()) {
		return xerrors.Errorf("download of %d bytes from %s resulted in CommP %02X, expected %02X", resp.ContentLength, task.URL, cp, task.Comm.Digest())
	}

	return nil
}

// zeroRegion fills a buffer region with zeros efficiently
func zeroRegion(t []byte) {
	// lazy 32MiB zero-filler
	zeroez := make([]byte, 32<<20)
	var off int
	remain := len(t)
	for remain > 0 {
		segLen := len(zeroez)
		if remain < segLen {
			segLen = remain
		}
		copy(t[off:off+segLen], zeroez)
		off += segLen
		remain -= segLen
	}
}
