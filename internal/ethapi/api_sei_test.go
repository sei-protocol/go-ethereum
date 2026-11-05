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

package ethapi

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus/beacon"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"
)

func TestDoEstimateGasAfterCalls(t *testing.T) {
	t.Parallel()
	var (
		accounts = newAccounts(2)
		genesis  = &core.Genesis{
			Config: params.MergedTestChainConfig,
			Alloc:  types.GenesisAlloc{accounts[0].addr: {Balance: big.NewInt(params.Ether)}},
		}
		backend = newTestBackend(t, 1, genesis, beacon.New(ethash.NewFaker()), func(i int, b *core.BlockGen) { b.SetPoS() })
		latest  = rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber)
		value   = (*hexutil.Big)(big.NewInt(1000))
		fund    = (*hexutil.Big)(big.NewInt(params.GWei))
	)
	// The unfunded account cannot send value on its own.
	transfer := TransactionArgs{From: &accounts[1].addr, To: &accounts[0].addr, Value: value}
	if _, err := DoEstimateGasAfterCalls(context.Background(), backend, transfer, nil, latest, nil, 0, 0); err == nil {
		t.Fatal("expected estimation to fail without prior funding")
	}
	// After a funding call it can.
	calls := []TransactionArgs{{From: &accounts[0].addr, To: &accounts[1].addr, Value: fund}}
	transfer = TransactionArgs{From: &accounts[1].addr, To: &accounts[0].addr, Value: value}
	got, err := DoEstimateGasAfterCalls(context.Background(), backend, transfer, calls, latest, nil, 0, 0)
	if err != nil {
		t.Fatalf("estimate after calls: %v", err)
	}
	if uint64(got) != params.TxGas {
		t.Fatalf("estimate: have %d, want %d", got, params.TxGas)
	}
}
