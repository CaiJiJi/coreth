// (c) 2024, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//go:build test

// warptest exposes common functionality for testing the warp package.
package warptest

import (
	"context"
	"slices"

	"github.com/CaiJiJi/avalanchego/database"
	"github.com/CaiJiJi/avalanchego/ids"
	"github.com/CaiJiJi/avalanchego/snow/consensus/snowman"
	"github.com/CaiJiJi/avalanchego/snow/consensus/snowman/snowmantest"
	"github.com/CaiJiJi/avalanchego/snow/snowtest"
)

// EmptyBlockClient returns an error if a block is requested
var EmptyBlockClient BlockClient = MakeBlockClient()

type BlockClient func(ctx context.Context, blockID ids.ID) (snowman.Block, error)

func (f BlockClient) GetAcceptedBlock(ctx context.Context, blockID ids.ID) (snowman.Block, error) {
	return f(ctx, blockID)
}

// MakeBlockClient returns a new BlockClient that returns the provided blocks.
// If a block is requested that isn't part of the provided blocks, an error is
// returned.
func MakeBlockClient(blkIDs ...ids.ID) BlockClient {
	return func(_ context.Context, blkID ids.ID) (snowman.Block, error) {
		if !slices.Contains(blkIDs, blkID) {
			return nil, database.ErrNotFound
		}

		return &snowmantest.Block{
			Decidable: snowtest.Decidable{
				IDV:    blkID,
				Status: snowtest.Accepted,
			},
		}, nil
	}
}
