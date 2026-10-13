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

// Package export re-exports the internal eth API symbols used by sei-chain.
package export

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/internal/ethapi"
	"github.com/ethereum/go-ethereum/internal/ethapi/override"
	"github.com/ethereum/go-ethereum/params"
)

type (
	Backend               = ethapi.Backend
	RPCTransaction        = ethapi.RPCTransaction
	SignTransactionResult = ethapi.SignTransactionResult
	TransactionArgs       = ethapi.TransactionArgs
	StateOverride         = override.StateOverride
	BlockOverrides        = override.BlockOverrides
	ChainContextBackend   = ethapi.ChainContextBackend
)

var (
	NewRPCPendingTransaction = ethapi.NewRPCPendingTransaction
	AccessList               = ethapi.AccessList
	DoCall                   = ethapi.DoCall
	DoEstimateGas            = ethapi.DoEstimateGas
	DoEstimateGasAfterCalls  = ethapi.DoEstimateGasAfterCalls
)

// NewRPCTransaction returns the RPC representation of tx without blockTimestamp.
func NewRPCTransaction(tx *types.Transaction, blockHash common.Hash, blockNumber uint64, blockTime uint64, index uint64, baseFee *big.Int, config *params.ChainConfig) *RPCTransaction {
	result := ethapi.NewRPCTransaction(tx, blockHash, blockNumber, blockTime, index, baseFee, config)
	result.BlockTimestamp = nil
	return result
}

// NewRPCTransactionFromBlockIndex returns the RPC representation of the transaction at index in b
// without blockTimestamp, or nil if index is out of range.
func NewRPCTransactionFromBlockIndex(b *types.Block, index uint64, config *params.ChainConfig) *RPCTransaction {
	result := ethapi.NewRPCTransactionFromBlockIndex(b, index, config)
	if result != nil {
		result.BlockTimestamp = nil
	}
	return result
}
