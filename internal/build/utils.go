package build

import (
	"bytes"
	"context"

	"github.com/moby/buildkit/client/llb"
	// "github.com/moby/buildkit/frontend/dockerui"
	"github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/patternmatcher/ignorefile"
	"github.com/pkg/errors"
)

func getExcludes(ctx context.Context, c client.Client) (excludes []string, err error) {
	// LLB
	state := llb.Local(localNameContext,
		llb.SessionID(c.BuildOpts().SessionID),
		llb.FollowPaths([]string{dockerignoreFilename}),
		llb.SharedKeyHint(localNameContext+"-"+dockerignoreFilename),
		// dockerui.WithInternalName("load "+dockerignoreFilename),
	)
	def, err := state.Marshal(ctx)
	if err != nil {
		return nil, err
	}
	// Solve
	res, err := c.Solve(ctx, client.SolveRequest{
		Definition: def.ToPB(),
	})
	if err != nil {
		return nil, err
	}
	// Read
	data, _ := res.Ref.ReadFile(ctx, client.ReadRequest{
		Filename: dockerignoreFilename,
	})
	if data == nil {
		excludes = []string{}
	} else {
		excludes, err = ignorefile.ReadAll(bytes.NewBuffer(data))
		if excludes == nil || err != nil {
			return nil, errors.Wrap(err, "failed to parse dockerignore")
		}
	}
	return excludes, nil
}