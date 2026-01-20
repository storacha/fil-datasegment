package dlass

import (
	"net/url"

	"code.riba.cloud/go/toolbox-interplanetary/fil"
	"code.riba.cloud/go/toolbox/cmn"
	"github.com/ipfs/go-cid"
)

// WrCommP wraps a fil.CommP for JSON unmarshalling
type WrCommP struct {
	fil.CommP
}

func (out *WrCommP) UnmarshalJSON(b []byte) error {
	c, err := cid.Decode(string(b[1 : len(b)-1]))
	if err != nil {
		return cmn.WrErr(err)
	}
	p, err := fil.CommPFromPCidV2(c)
	if err != nil {
		return cmn.WrErr(err)
	}
	*out = WrCommP{p}
	return nil
}

// WrURL wraps a url.URL for JSON unmarshalling
type WrURL struct {
	url.URL
}

func (out *WrURL) UnmarshalJSON(b []byte) error {
	u, err := url.Parse(string(b[1 : len(b)-1]))
	if err != nil {
		return cmn.WrErr(err)
	}
	*out = WrURL{*u}
	return nil
}
