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

package vm

import (
	"errors"
	"math/big"
	"reflect"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// seiAmsterdamTestConfig returns a merged Osaka config, with Sei Amsterdam active if sei is set.
func seiAmsterdamTestConfig(sei bool) *params.ChainConfig {
	cfg := *params.MergedTestChainConfig
	if sei {
		zero := uint64(0)
		cfg.SeiAmsterdamTime = &zero
	}
	return &cfg
}

func seiAmsterdamTestRules(sei bool) params.Rules {
	return seiAmsterdamTestConfig(sei).Rules(big.NewInt(1), true, 0)
}

func TestSeiAmsterdamRules(t *testing.T) {
	rules := seiAmsterdamTestRules(true)
	if !rules.IsOsaka || !rules.IsSeiAmsterdam || rules.IsAmsterdam {
		t.Fatalf("rules: osaka %v seiAmsterdam %v amsterdam %v", rules.IsOsaka, rules.IsSeiAmsterdam, rules.IsAmsterdam)
	}
	if err := seiAmsterdamTestConfig(true).CheckConfigForkOrder(); err != nil {
		t.Fatalf("fork order: %v", err)
	}

	// Sei Amsterdam requires Osaka.
	noOsaka := seiAmsterdamTestConfig(true)
	noOsaka.OsakaTime = nil
	if noOsaka.Rules(big.NewInt(1), true, 0).IsSeiAmsterdam {
		t.Fatal("IsSeiAmsterdam active without Osaka")
	}
	if err := noOsaka.CheckConfigForkOrder(); err == nil {
		t.Fatal("expected fork order error without Osaka")
	}
	early := seiAmsterdamTestConfig(true)
	osaka := uint64(10)
	early.OsakaTime = &osaka
	if err := early.CheckConfigForkOrder(); err == nil {
		t.Fatal("expected fork order error with Sei Amsterdam before Osaka")
	}
}

// TestSeiAmsterdamInstructionSet checks the Sei Amsterdam table is Osaka plus EIP-8024 only.
func TestSeiAmsterdamInstructionSet(t *testing.T) {
	sei, err := LookupInstructionSet(seiAmsterdamTestRules(true))
	if err != nil {
		t.Fatal(err)
	}
	osaka, err := LookupInstructionSet(seiAmsterdamTestRules(false))
	if err != nil {
		t.Fatal(err)
	}
	fn := func(f any) uintptr {
		v := reflect.ValueOf(f)
		if v.IsNil() {
			return 0
		}
		return v.Pointer()
	}
	for i := range sei {
		op := OpCode(i)
		s, o := sei[i], osaka[i]
		switch op {
		case DUPN, SWAPN, EXCHANGE:
			if s.undefined || !o.undefined {
				t.Errorf("%v: sei undefined %v, osaka undefined %v", op, s.undefined, o.undefined)
			}
			continue
		}
		if s.undefined != o.undefined || s.constantGas != o.constantGas ||
			fn(s.dynamicGas) != fn(o.dynamicGas) || fn(s.execute) != fn(o.execute) {
			t.Errorf("%v differs from Osaka", op)
		}
	}
	if !sei[SLOTNUM].undefined {
		t.Error("SLOTNUM must stay undefined")
	}
	if sei[CLZ].undefined {
		t.Error("CLZ must be defined")
	}
}

func TestSeiAmsterdamCodeSize(t *testing.T) {
	sei, osaka := seiAmsterdamTestRules(true), seiAmsterdamTestRules(false)
	for _, tc := range []struct {
		name  string
		check func(*params.Rules, uint64) error
		rules params.Rules
		size  uint64
		fails bool
	}{
		{"code at amsterdam limit", CheckMaxCodeSize, sei, params.MaxCodeSizeAmsterdam, false},
		{"code over amsterdam limit", CheckMaxCodeSize, sei, params.MaxCodeSizeAmsterdam + 1, true},
		{"code over osaka limit", CheckMaxCodeSize, osaka, 24577, true},
		{"initcode at amsterdam limit", CheckMaxInitCodeSize, sei, params.MaxInitCodeSizeAmsterdam, false},
		{"initcode over amsterdam limit", CheckMaxInitCodeSize, sei, params.MaxInitCodeSizeAmsterdam + 1, true},
		{"initcode over osaka limit", CheckMaxInitCodeSize, osaka, 49153, true},
	} {
		err := tc.check(&tc.rules, tc.size)
		if tc.fails != (err != nil) {
			t.Errorf("%s: err %v", tc.name, err)
		}
		if err != nil && !errors.Is(err, ErrMaxCodeSizeExceeded) && !errors.Is(err, ErrMaxInitCodeSizeExceeded) {
			t.Errorf("%s: unexpected error %v", tc.name, err)
		}
	}
}

// selfDestructRun creates a contract that self-destructs to beneficiary (or itself if zero)
// with a 10 wei endowment, and returns the state, contract address and rules.
func selfDestructRun(t *testing.T, sei bool, beneficiary common.Address) (*state.StateDB, common.Address, params.Rules) {
	t.Helper()
	statedb, _ := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	origin := common.HexToAddress("0x1000")
	statedb.AddBalance(origin, uint256.NewInt(100), tracing.BalanceChangeUnspecified)

	var initcode []byte
	if beneficiary == (common.Address{}) {
		initcode = []byte{byte(ADDRESS), byte(SELFDESTRUCT)}
	} else {
		initcode = append([]byte{byte(PUSH20)}, beneficiary.Bytes()...)
		initcode = append(initcode, byte(SELFDESTRUCT))
	}
	cfg := seiAmsterdamTestConfig(sei)
	random := common.Hash{}
	blockCtx := BlockContext{
		CanTransfer: func(db StateDB, addr common.Address, amount *uint256.Int) bool {
			return db.GetBalance(addr).Cmp(amount) >= 0
		},
		Transfer: func(db StateDB, from, to common.Address, amount *uint256.Int, _ *params.Rules) {
			db.SubBalance(from, amount, tracing.BalanceChangeTransfer)
			db.AddBalance(to, amount, tracing.BalanceChangeTransfer)
		},
		BlockNumber: big.NewInt(1),
		Random:      &random,
	}
	evm := NewEVM(blockCtx, statedb, cfg, Config{})
	statedb.Prepare(evm.GetRules(), origin, common.Address{}, nil, ActivePrecompiles(evm.GetRules()), nil)
	_, addr, _, err := evm.Create(origin, initcode, NewGasBudget(100_000, 0), uint256.NewInt(10))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return statedb, addr, evm.GetRules()
}

func TestSeiAmsterdamSelfDestructToSelfKeepsBalance(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sei     bool
		balance uint64
	}{
		{"osaka burns", false, 0},
		{"sei amsterdam keeps", true, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			statedb, addr, rules := selfDestructRun(t, tc.sei, common.Address{})
			if got := statedb.GetBalance(addr).Uint64(); got != tc.balance {
				t.Fatalf("balance after SELFDESTRUCT: have %d want %d", got, tc.balance)
			}
			statedb.Finalise(rules)
			if got := statedb.Exist(addr); got != tc.sei {
				t.Fatalf("exists after finalise: have %v want %v", got, tc.sei)
			}
			if got := statedb.GetBalance(addr).Uint64(); got != tc.balance {
				t.Fatalf("balance after finalise: have %d want %d", got, tc.balance)
			}
			if statedb.GetNonce(addr) != 0 || len(statedb.GetCode(addr)) != 0 {
				t.Fatal("nonce and code must be cleared")
			}
			if len(statedb.Logs()) != 0 {
				t.Fatalf("unexpected logs: %v", statedb.Logs())
			}
		})
	}
}

func TestSeiAmsterdamSelfDestructTransferLog(t *testing.T) {
	beneficiary := common.HexToAddress("0x2000")
	for _, tc := range []struct {
		name string
		sei  bool
		logs int
	}{
		{"osaka", false, 0},
		{"sei amsterdam", true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			statedb, addr, _ := selfDestructRun(t, tc.sei, beneficiary)
			logs := statedb.Logs()
			if len(logs) != tc.logs {
				t.Fatalf("logs: have %d want %d", len(logs), tc.logs)
			}
			if tc.logs == 0 {
				return
			}
			want := types.EthTransferLog(addr, beneficiary, uint256.NewInt(10))
			if logs[0].Address != want.Address || !reflect.DeepEqual(logs[0].Topics, want.Topics) || !reflect.DeepEqual(logs[0].Data, want.Data) {
				t.Fatalf("log: have %+v want %+v", logs[0], want)
			}
		})
	}
}
