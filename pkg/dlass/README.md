# dlass - FRC58 Datasegment Processing Library

`dlass` is a Go library that provides core functionality for downloading and assembling FRC58-formatted Filecoin aggregates. It separates the business logic from CLI concerns, allowing the functionality to be used as a reusable library in other applications.

## Features

- Download and validate individual data segments via HTTP
- Verify segment integrity using CommP (Commitment hash) validation
- Support for parallel processing with configurable concurrency
- Retry logic for failed downloads
- Segment timeout configuration
- Zero-fill regions for padding
- JSON unmarshalling support for CID-based manifests

## Types

### `Agg`
Represents an FRC58 aggregate manifest containing metadata and a list of pieces.

```go
type Agg struct {
    FRC58CommP *WrCommP `json:"frc58_aggregate"`
    PieceList  []Piece  `json:"piece_list"`
}
```

### `Piece`
Represents a single piece within an aggregate, with its commitment hash and download sources.

```go
type Piece struct {
    PcidV2  WrCommP `json:"pcid_v2"`
    Sources []WrURL `json:"sources"`
}
```

### `PieceTask`
Internal representation of a task for processing a piece.

```go
type PieceTask struct {
    StartOffset int        // Offset in output buffer
    PayloadSize int        // Size of payload
    SegmentSize int        // Total segment size (including padding)
    Comm        *fil.CommP // CommitmentP hash (nil for zero-fill regions)
    URL         *url.URL   // Download URL (nil for zero-fill regions)
}
```

### `ProcessorConfig`
Configuration for the Processor.

```go
type ProcessorConfig struct {
    MaxRetries      int           // Maximum number of retries
    RetryWaitMin    time.Duration // Minimum wait between retries
    RetryWaitMax    time.Duration // Maximum wait between retries
    SegmentTimeout  time.Duration // Timeout per segment
}
```

## High-Level API: AssembleAggregate

For most use cases, you'll want to use the `AssembleAggregate` function, which handles the entire assembly process from manifest to file:

```go
config := dlass.AssemblerConfig{
    ProcessorConfig: dlass.DefaultProcessorConfig(),
    MaxConcurrency:  5,
    OverwriteFile:   false,
    RandomizeOrder:  true,
    OutputFilename:  "", // Uses aggregate CID if empty
}

filename, err := dlass.AssembleAggregate(
    ctx,
    &manifest,
    config,
    nil, // optional progress callback
)
if err != nil {
    log.Fatal(err)
}
log.Printf("Assembly complete: %s", filename)
```

### With Progress Reporting

```go
filename, err := dlass.AssembleAggregate(
    ctx,
    &manifest,
    config,
    func(stats dlass.AssemblyStats) {
        fmt.Printf("Downloaded: %d/%d segments, %.2f GiB\n",
            stats.DownloadedSegments,
            stats.TotalSegments,
            float64(stats.TotalBytes)/(1<<30),
        )
    },
)
```

### AssemblerConfig Options

- `ProcessorConfig`: Configuration for the underlying HTTP processor (retries, timeouts, etc.)
- `MaxConcurrency`: Number of parallel downloads (default: 5)
- `OverwriteFile`: Whether to overwrite existing output file (default: false)
- `RandomizeOrder`: Whether to randomize download order (default: true); if false, downloads in ascending size order
- `OutputFilename`: Path for output file (if empty, uses aggregate CID with `.frc58` extension)

### AssemblyStats

Passed to the progress callback with current assembly status:

```go
type AssemblyStats struct {
    TotalSegments      int64  // Total number of segments to download
    DownloadedSegments int64  // Number of segments successfully downloaded
    ExistingSegments   int64  // Number of segments that already existed
    TotalBytes         int64  // Total payload bytes
}
```

## Low-Level API: Processor

For more fine-grained control, you can use the `Processor` type which handles downloading and validating individual segments.

### Creating a Processor

```go
import "github.com/storacha/fil-datasegment/pkg/dlass"

config := dlass.ProcessorConfig{
    MaxRetries:    5,
    RetryWaitMin:  3 * time.Second,
    RetryWaitMax:  15 * time.Second,
    SegmentTimeout: 10 * time.Minute,
}
processor := dlass.NewProcessor(config)
```

Or use defaults:

```go
processor := dlass.NewProcessor(dlass.DefaultProcessorConfig())
```

### Processing a Piece

```go
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
defer cancel()

task := dlass.PieceTask{
    StartOffset: 0,
    PayloadSize: 1024,
    SegmentSize: 1024,
    Comm:        &commP,
    URL:         downloadURL,
}

// Buffer must be pre-allocated with size >= task.SegmentSize
buffer := make([]byte, task.SegmentSize)

err := processor.ProcessPiece(ctx, task, buffer, false)
if err != nil {
    log.Fatal(err)
}
```

## JSON Unmarshalling

The library provides special wrapper types that support unmarshalling from JSON:

- `WrCommP` - Unmarshals a CID string into a `fil.CommP`
- `WrURL` - Unmarshals a URL string into a `net.URL`

Example:

```go
var manifest dlass.Agg
if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
    log.Fatal(err)
}
```

## Example Usage

### Simple Assembly (Recommended)

```go
package main

import (
    "context"
    "encoding/json"
    "fmt"
    "log"
    "os"

    "github.com/storacha/fil-datasegment/pkg/dlass"
)

func main() {
    // Load manifest
    manifestFile, err := os.Open("manifest.json")
    if err != nil {
        log.Fatal(err)
    }
    var manifest dlass.Agg
    if err := json.NewDecoder(manifestFile).Decode(&manifest); err != nil {
        log.Fatal(err)
    }
    manifestFile.Close()

    // Configure assembly
    config := dlass.AssemblerConfig{
        ProcessorConfig: dlass.DefaultProcessorConfig(),
        MaxConcurrency:  5,
        OverwriteFile:   false,
        RandomizeOrder:  true,
    }

    // Assembly with progress reporting
    ctx := context.Background()
    filename, err := dlass.AssembleAggregate(
        ctx,
        &manifest,
        config,
        func(stats dlass.AssemblyStats) {
            fmt.Printf("Progress: %d/%d segments, %.2f GiB downloaded\n",
                stats.DownloadedSegments,
                stats.TotalSegments,
                float64(stats.TotalBytes)/(1<<30),
            )
        },
    )
    if err != nil {
        log.Fatal(err)
    }

    fmt.Printf("Assembly complete: %s\n", filename)
}
```

### Advanced: Manual Processing

For cases where you need custom logic, use the processor directly:

```go
// Create processor with custom config
config := dlass.ProcessorConfig{
    MaxRetries:     5,
    RetryWaitMin:   3 * time.Second,
    RetryWaitMax:   15 * time.Second,
    SegmentTimeout: 10 * time.Minute,
}
processor := dlass.NewProcessor(config)

// Process each piece manually
for _, piece := range manifest.PieceList {
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)

    buffer := make([]byte, piece.PcidV2.PieceInfo().Size.Unpadded())

    task := dlass.PieceTask{
        Comm:        &piece.PcidV2.CommP,
        URL:         &piece.Sources[0].URL,
        SegmentSize: len(buffer),
        PayloadSize: len(buffer),
    }

    err := processor.ProcessPiece(ctx, task, buffer, false)
    if err != nil {
        log.Printf("Failed to process piece: %v", err)
    }

    cancel()
}
```

## Architecture

The library separates:

- **Core Logic** (`pkg/dlass/`): Reusable processing functionality
  - `processor.go`: Main processing logic
  - `types.go`: Public data structures
  - `wrappers.go`: JSON marshalling support

- **CLI Layer** (`dlass.go`, `main.go`): Command-line interface and orchestration
  - Command-line flag parsing
  - File I/O and memory-mapped operations
  - Progress reporting
  - Global state management

This separation allows other applications to use the core library functionality without depending on CLI infrastructure.
