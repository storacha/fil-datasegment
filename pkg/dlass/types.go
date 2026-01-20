package dlass

import (
	"net/url"

	"code.riba.cloud/go/toolbox-interplanetary/fil"
)

// Agg represents an FRC58 aggregate with a list of pieces
type Agg struct {
	FRC58CommP *WrCommP `json:"frc58_aggregate"`
	PieceList  []Piece  `json:"piece_list"`
}

// Piece represents a single piece in an aggregate
type Piece struct {
	PcidV2  WrCommP `json:"pcid_v2"`
	Sources []WrURL `json:"sources"`
}

// PieceTask represents an internal task for processing a piece
type PieceTask struct {
	StartOffset int        // offset in the output buffer where this piece should be written
	PayloadSize int        // size of the payload
	SegmentSize int        // total segment size (including padding)
	Comm        *fil.CommP // CommitmentP hash, nil for zero-fill regions
	URL         *url.URL   // source URL for downloading, nil for zero-fill regions
}
