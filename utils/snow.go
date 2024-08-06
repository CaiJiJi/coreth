// (c) 2019-2020, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package utils

import (
	"github.com/CaiJiJi/avalanchego/api/metrics"
	"github.com/CaiJiJi/avalanchego/ids"
	"github.com/CaiJiJi/avalanchego/snow"
	"github.com/CaiJiJi/avalanchego/snow/validators/validatorstest"
	"github.com/CaiJiJi/avalanchego/utils/crypto/bls"
	"github.com/CaiJiJi/avalanchego/utils/logging"
)

func TestSnowContext() *snow.Context {
	sk, err := bls.NewSecretKey()
	if err != nil {
		panic(err)
	}
	pk := bls.PublicFromSecretKey(sk)
	return &snow.Context{
		NetworkID:      0,
		SubnetID:       ids.Empty,
		ChainID:        ids.Empty,
		NodeID:         ids.EmptyNodeID,
		PublicKey:      pk,
		Log:            logging.NoLog{},
		BCLookup:       ids.NewAliaser(),
		Metrics:        metrics.NewMultiGatherer(),
		ChainDataDir:   "",
		ValidatorState: &validatorstest.State{},
	}
}
