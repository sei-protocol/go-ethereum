// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package eth

import (
	"context"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/eth/tracers"
	"github.com/ethereum/go-ethereum/eth/tracers/tracersutils"
	"github.com/ethereum/go-ethereum/internal/ethapi"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"
)

var _ tracers.Backend = (*EthAPIBackend)(nil)

// BlockWithTraceMetadataByNumber implements tracers.SeiBackend. Every
// transaction of a geth block is traced, so there is no metadata.
func (b *EthAPIBackend) BlockWithTraceMetadataByNumber(ctx context.Context, number rpc.BlockNumber) (*types.Block, []tracersutils.TraceBlockMetadata, error) {
	block, err := b.BlockByNumber(ctx, number)
	return block, nil, err
}

// BlockWithTraceMetadataByHash implements tracers.SeiBackend.
func (b *EthAPIBackend) BlockWithTraceMetadataByHash(ctx context.Context, hash common.Hash) (*types.Block, []tracersutils.TraceBlockMetadata, error) {
	block, err := b.BlockByHash(ctx, hash)
	return block, nil, err
}

// ChainConfigAtHeight implements tracers.SeiBackend. The configuration of a
// geth node does not change with height.
func (b *EthAPIBackend) ChainConfigAtHeight(height int64) *params.ChainConfig {
	return b.ChainConfig()
}

// PrepareTx implements tracers.SeiBackend; a geth node needs no preparation.
func (b *EthAPIBackend) PrepareTx(statedb vm.StateDB, tx *types.Transaction) error {
	return nil
}

// GetBlockContext implements tracers.SeiBackend.
func (b *EthAPIBackend) GetBlockContext(ctx context.Context, block *types.Block, statedb vm.StateDB, backend ethapi.ChainContextBackend) (vm.BlockContext, error) {
	return core.NewEVMBlockContext(block.Header(), ethapi.NewChainContext(ctx, backend), nil), nil
}
