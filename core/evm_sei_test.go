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

package core

import (
	"testing"

	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// TestTransferLogSeiAmsterdam checks EIP-7708 transfer logs follow IsSeiAmsterdam.
func TestTransferLogSeiAmsterdam(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rules  params.Rules
		amount uint64
		self   bool
		logs   int
	}{
		{name: "osaka", rules: params.Rules{IsOsaka: true}, amount: 5},
		{name: "sei amsterdam", rules: params.Rules{IsOsaka: true, IsSeiAmsterdam: true}, amount: 5, logs: 1},
		{name: "zero value", rules: params.Rules{IsOsaka: true, IsSeiAmsterdam: true}},
		{name: "to self", rules: params.Rules{IsOsaka: true, IsSeiAmsterdam: true}, amount: 5, self: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			statedb, _ := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
			statedb.AddBalance(seiTestSender, uint256.NewInt(10), tracing.BalanceChangeUnspecified)
			to := seiTestTo
			if tc.self {
				to = seiTestSender
			}
			amount := uint256.NewInt(tc.amount)
			Transfer(statedb, seiTestSender, to, amount, &tc.rules)
			logs := statedb.Logs()
			if len(logs) != tc.logs {
				t.Fatalf("logs: have %d want %d", len(logs), tc.logs)
			}
			if tc.logs == 1 && logs[0].Topics[0] != params.EthTransferLogEvent {
				t.Fatalf("topic: have %v want %v", logs[0].Topics[0], params.EthTransferLogEvent)
			}
		})
	}
}
