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

// Sei extensions to the eth API, exposed to sei-chain through package export.

package ethapi

import (
	"context"
	"errors"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/eth/gasestimator"
	"github.com/ethereum/go-ethereum/internal/ethapi/override"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"
)

// NewRPCTransaction returns a transaction that will serialize to the RPC
// representation, with the given location metadata set (if available).
func NewRPCTransaction(tx *types.Transaction, blockHash common.Hash, blockNumber uint64, blockTime uint64, index uint64, baseFee *big.Int, config *params.ChainConfig) *RPCTransaction {
	return newRPCTransaction(tx, blockHash, blockNumber, blockTime, index, baseFee, config)
}

// NewRPCTransactionFromBlockIndex returns the transaction at index in b in its
// RPC representation.
func NewRPCTransactionFromBlockIndex(b *types.Block, index uint64, config *params.ChainConfig) *RPCTransaction {
	return newRPCTransactionFromBlockIndex(b, index, config)
}

// SetDefaults fills in default values for unspecified tx fields. Blob sidecars
// are not accepted.
func (args *TransactionArgs) SetDefaults(ctx context.Context, b Backend) error {
	return args.setDefaults(ctx, b, sidecarConfig{})
}

// DoEstimateGasAfterCalls estimates the gas of args after first applying calls,
// in order, on top of the state at blockNrOrHash. The state overrides apply
// before the first call.
func DoEstimateGasAfterCalls(ctx context.Context, b Backend, args TransactionArgs, calls []TransactionArgs, blockNrOrHash rpc.BlockNumberOrHash, overrides *override.StateOverride, timeout time.Duration, gasCap uint64) (hexutil.Uint64, error) {
	state, header, err := b.StateAndHeaderByNumberOrHash(ctx, blockNrOrHash)
	if state == nil || err != nil {
		return 0, err
	}
	for _, call := range calls {
		if _, err := doCall(ctx, b, call, state, header, overrides, nil, timeout, gasCap); err != nil {
			return 0, err
		}
		overrides = nil
	}
	if overrides != nil {
		// No calls were made, so the overrides still need applying.
		blockCtx := core.NewEVMBlockContext(header, NewChainContext(ctx, b), nil)
		rules := b.ChainConfig().Rules(blockCtx.BlockNumber, blockCtx.Random != nil, blockCtx.Time)
		if err := overrides.Apply(state, vm.ActivePrecompiledContracts(rules)); err != nil {
			return 0, err
		}
	}
	opts := &gasestimator.Options{
		Config:     b.ChainConfig(),
		Chain:      NewChainContext(ctx, b),
		Header:     header,
		State:      state,
		ErrorRatio: estimateGasErrorRatio,

		CustomPrecompiles: b.GetCustomPrecompiles(header.Number.Int64()),
	}
	if args.Gas == nil {
		args.Gas = new(hexutil.Uint64)
	}
	if err := args.CallDefaults(gasCap, header.BaseFee, b.ChainConfig().ChainID); err != nil {
		return 0, err
	}
	call := args.ToMessage(header.BaseFee, true)

	estimate, revert, err := gasestimator.Estimate(ctx, call, opts, gasCap)
	if err != nil {
		if errors.Is(err, vm.ErrExecutionReverted) {
			return 0, newRevertError(revert)
		}
		return 0, err
	}
	return hexutil.Uint64(estimate), nil
}
