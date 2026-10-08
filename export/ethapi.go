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
	"github.com/ethereum/go-ethereum/internal/ethapi"
	"github.com/ethereum/go-ethereum/internal/ethapi/override"
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
	NewRPCTransaction               = ethapi.NewRPCTransaction
	NewRPCTransactionFromBlockIndex = ethapi.NewRPCTransactionFromBlockIndex
	NewRPCPendingTransaction        = ethapi.NewRPCPendingTransaction
	AccessList                      = ethapi.AccessList
	DoCall                          = ethapi.DoCall
	DoEstimateGas                   = ethapi.DoEstimateGas
	DoEstimateGasAfterCalls         = ethapi.DoEstimateGasAfterCalls
)
