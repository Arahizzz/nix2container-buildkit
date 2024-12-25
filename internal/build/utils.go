package build

import (
	"github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver/pb"
)

func IsMergeSupported(c client.Client) bool {
	caps := c.BuildOpts().LLBCaps
	err := (&caps).Supports(pb.CapMergeOp)
	return err == nil
}

func IsDiffSupported(c client.Client) bool {
	caps := c.BuildOpts().LLBCaps
	err := (&caps).Supports(pb.CapDiffOp)
	return err == nil
}
