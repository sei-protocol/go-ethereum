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

package tracers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/eth/tracers/tracersutils"
	"github.com/ethereum/go-ethereum/internal/ethapi"
	"github.com/ethereum/go-ethereum/internal/ethapi/override"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/holiman/uint256"
)

// SeiBackend methods of the upstream test backend, behaving like a geth node.

func (b *testBackend) BlockWithTraceMetadataByNumber(ctx context.Context, number rpc.BlockNumber) (*types.Block, []tracersutils.TraceBlockMetadata, error) {
	block, err := b.BlockByNumber(ctx, number)
	return block, nil, err
}

func (b *testBackend) BlockWithTraceMetadataByHash(ctx context.Context, hash common.Hash) (*types.Block, []tracersutils.TraceBlockMetadata, error) {
	block, err := b.BlockByHash(ctx, hash)
	return block, nil, err
}

func (b *testBackend) ChainConfigAtHeight(int64) *params.ChainConfig {
	return b.chainConfig
}

func (b *testBackend) GetCustomPrecompiles(int64) map[common.Address]vm.CustomPrecompiledContract {
	return nil
}

func (b *testBackend) PrepareTx(vm.StateDB, *types.Transaction) error {
	return nil
}

func (b *testBackend) GetBlockContext(ctx context.Context, block *types.Block, statedb vm.StateDB, backend ethapi.ChainContextBackend) (vm.BlockContext, error) {
	return core.NewEVMBlockContext(block.Header(), ethapi.NewChainContext(ctx, backend), nil), nil
}

// seiTestBackend is a testBackend which serves trace metadata and custom
// precompiles like Sei does.
type seiTestBackend struct {
	*testBackend
	metadata    func(block *types.Block) []tracersutils.TraceBlockMetadata
	precompiles map[common.Address]vm.CustomPrecompiledContract
	prepared    atomic.Int32
}

func (b *seiTestBackend) BlockWithTraceMetadataByNumber(ctx context.Context, number rpc.BlockNumber) (*types.Block, []tracersutils.TraceBlockMetadata, error) {
	block, err := b.BlockByNumber(ctx, number)
	if block == nil || err != nil || b.metadata == nil {
		return block, nil, err
	}
	return block, b.metadata(block), nil
}

func (b *seiTestBackend) BlockWithTraceMetadataByHash(ctx context.Context, hash common.Hash) (*types.Block, []tracersutils.TraceBlockMetadata, error) {
	block, err := b.BlockByHash(ctx, hash)
	if block == nil || err != nil || b.metadata == nil {
		return block, nil, err
	}
	return block, b.metadata(block), nil
}

func (b *seiTestBackend) GetCustomPrecompiles(int64) map[common.Address]vm.CustomPrecompiledContract {
	return b.precompiles
}

func (b *seiTestBackend) PrepareTx(vm.StateDB, *types.Transaction) error {
	b.prepared.Add(1)
	return nil
}

// newMetadataTestBackend creates a chain whose first block holds three
// transfers from accounts[0], with nonces 0, 1 and 2.
func newMetadataTestBackend(t *testing.T) (*seiTestBackend, []Account) {
	accounts := newAccounts(3)
	genesis := &core.Genesis{
		Config: params.TestChainConfig,
		Alloc: types.GenesisAlloc{
			accounts[0].addr: {Balance: big.NewInt(params.Ether)},
			accounts[1].addr: {Balance: big.NewInt(params.Ether)},
		},
	}
	signer := types.HomesteadSigner{}
	backend := newTestBackend(t, 1, genesis, func(i int, b *core.BlockGen) {
		for nonce := uint64(0); nonce < 3; nonce++ {
			tx, _ := types.SignTx(types.NewTx(&types.LegacyTx{
				Nonce:    nonce,
				To:       &accounts[1].addr,
				Value:    big.NewInt(1000),
				Gas:      params.TxGas,
				GasPrice: b.BaseFee(),
			}), signer, accounts[0].key)
			b.AddTx(tx)
		}
	})
	return &seiTestBackend{testBackend: backend}, accounts
}

func TestTraceBlockWithMetadata(t *testing.T) {
	t.Parallel()

	backend, accounts := newMetadataTestBackend(t)
	defer backend.teardown()

	var runs atomic.Int32
	// The middle transaction is replayed by its TraceRunnable instead of being
	// traced. It stands in for a non-EVM transaction which changes the state:
	// it bumps the sender nonce, which the last transaction depends on.
	backend.metadata = func(block *types.Block) []tracersutils.TraceBlockMetadata {
		return []tracersutils.TraceBlockMetadata{
			{ShouldIncludeInTraceResult: true, IdxInEthBlock: 0},
			{TraceRunnable: func(statedb vm.StateDB) {
				runs.Add(1)
				nonce := statedb.GetNonce(accounts[0].addr)
				statedb.SetNonce(accounts[0].addr, nonce+1, tracing.NonceChangeUnspecified)
			}},
			{ShouldIncludeInTraceResult: true, IdxInEthBlock: 2},
		}
	}
	api := NewAPI(backend)
	block := backend.chain.GetBlockByNumber(1)
	txs := block.Transactions()

	check := func(name string, results []*TxTraceResult, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: trace failed: %v", name, err)
		}
		if len(results) != len(txs) {
			t.Fatalf("%s: have %d results, want %d", name, len(results), len(txs))
		}
		if results[1] != nil {
			t.Errorf("%s: excluded transaction has a result: %+v", name, results[1])
		}
		for _, i := range []int{0, 2} {
			if results[i] == nil {
				t.Fatalf("%s: missing result for transaction %d", name, i)
			}
			if results[i].TxHash != txs[i].Hash() {
				t.Errorf("%s: result %d hash mismatch: have %x, want %x", name, i, results[i].TxHash, txs[i].Hash())
			}
			if results[i].Error != "" {
				t.Errorf("%s: transaction %d failed: %s", name, i, results[i].Error)
			}
			want := `{"gas":21000,"failed":false,"returnValue":"0x","structLogs":[]}`
			if have, _ := json.Marshal(results[i].Result); string(have) != want {
				t.Errorf("%s: transaction %d result mismatch: have %s, want %s", name, i, have, want)
			}
		}
	}
	results, err := api.TraceBlockByNumber(context.Background(), 1, nil)
	check("by number", results, err)
	results, err = api.TraceBlockByHash(context.Background(), block.Hash(), nil)
	check("by hash", results, err)

	if have := runs.Load(); have != 2 {
		t.Errorf("TraceRunnable ran %d times, want 2", have)
	}
	if have := backend.prepared.Load(); have != 4 {
		t.Errorf("PrepareTx called %d times, want 4", have)
	}
}

func TestTraceBlockWithMetadataExcludesResults(t *testing.T) {
	t.Parallel()

	backend, _ := newMetadataTestBackend(t)
	defer backend.teardown()

	// Only the first transaction is part of the trace result; the others are
	// replayed by their TraceRunnable, which runs them on the traced state.
	var runs atomic.Int32
	backend.metadata = func(block *types.Block) []tracersutils.TraceBlockMetadata {
		md := []tracersutils.TraceBlockMetadata{{ShouldIncludeInTraceResult: true, IdxInEthBlock: 0}}
		for range block.Transactions()[1:] {
			md = append(md, tracersutils.TraceBlockMetadata{TraceRunnable: func(vm.StateDB) { runs.Add(1) }})
		}
		return md
	}
	results, err := NewAPI(backend).TraceBlockByNumber(context.Background(), 1, nil)
	if err != nil {
		t.Fatalf("trace failed: %v", err)
	}
	if results[0] == nil || results[0].Error != "" {
		t.Fatalf("unexpected result for transaction 0: %+v", results[0])
	}
	if results[1] != nil || results[2] != nil {
		t.Errorf("excluded transactions have results: %+v, %+v", results[1], results[2])
	}
	if have := runs.Load(); have != 2 {
		t.Errorf("TraceRunnable ran %d times, want 2", have)
	}

	// Metadata referencing a transaction outside the block is rejected.
	backend.metadata = func(*types.Block) []tracersutils.TraceBlockMetadata {
		return []tracersutils.TraceBlockMetadata{{ShouldIncludeInTraceResult: true, IdxInEthBlock: 3}}
	}
	if _, err := NewAPI(backend).TraceBlockByNumber(context.Background(), 1, nil); err == nil {
		t.Error("expected error for out of range metadata")
	}
}

func TestTraceBlockWithMetadataHonoursContext(t *testing.T) {
	t.Parallel()

	backend, _ := newMetadataTestBackend(t)
	defer backend.teardown()

	ctx, cancel := context.WithCancel(context.Background())
	backend.metadata = func(*types.Block) []tracersutils.TraceBlockMetadata {
		return []tracersutils.TraceBlockMetadata{
			{TraceRunnable: func(vm.StateDB) { cancel() }},
			{ShouldIncludeInTraceResult: true, IdxInEthBlock: 0},
		}
	}
	_, err := NewAPI(backend).TraceBlockByNumber(ctx, 1, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want %v, have %v", context.Canceled, err)
	}
}

// countingPrecompile is a custom precompile returning a fixed output.
type countingPrecompile struct {
	calls atomic.Int32
}

func (p *countingPrecompile) RequiredGas([]byte) uint64 { return 100 }

func (p *countingPrecompile) Run(evm *vm.EVM, sender common.Address, callingContract common.Address, input []byte, value *big.Int, readOnly bool, isFromDelegateCall bool, hooks *tracing.Hooks) ([]byte, error) {
	p.calls.Add(1)
	return []byte{0x42}, nil
}

// precompileTracer records, for every call, whether the target is a precompile
// according to the VMContext the tracer was started with.
type precompileTracer struct {
	precompiles []common.Address
	calls       map[common.Address]bool
}

func newPrecompileTracer(*Context, json.RawMessage, *params.ChainConfig) (*Tracer, error) {
	t := &precompileTracer{calls: make(map[common.Address]bool)}
	return &Tracer{
		Hooks: &tracing.Hooks{
			OnTxStart: func(env *tracing.VMContext, tx *types.Transaction, from common.Address) {
				t.precompiles = env.Precompiles
			},
			OnEnter: func(depth int, typ byte, from common.Address, to common.Address, input []byte, gas uint64, value *big.Int) {
				t.calls[to] = slices.Contains(t.precompiles, to)
			},
		},
		GetResult: func() (json.RawMessage, error) { return json.Marshal(t.calls) },
		Stop:      func(error) {},
	}, nil
}

func TestTraceCallCustomPrecompile(t *testing.T) {
	t.Parallel()

	backend, accounts := newMetadataTestBackend(t)
	defer backend.teardown()

	var (
		precompileAddr = common.HexToAddress("0x0000000000000000000000000000000000001001")
		precompile     = new(countingPrecompile)
		input          = hexutil.Bytes{0xde, 0xad, 0xbe, 0xef, 0x01}
		api            = NewAPI(backend)
		args           = ethapi.TransactionArgs{From: &accounts[0].addr, To: &precompileAddr, Input: &input}
		tracer         = "seiPrecompileTracer"
		nonce          = hexutil.Uint64(7)
	)
	backend.precompiles = map[common.Address]vm.CustomPrecompiledContract{precompileAddr: precompile}
	DefaultDirectory.Register(tracer, newPrecompileTracer, false)

	// The tracer is told about the custom precompile, which is executed, also
	// when the builtin precompiles are replaced because of state overrides.
	for i, config := range []*TraceCallConfig{
		{TraceConfig: TraceConfig{Tracer: &tracer}},
		{TraceConfig: TraceConfig{Tracer: &tracer}, StateOverrides: &override.StateOverride{accounts[1].addr: override.OverrideAccount{Nonce: &nonce}}},
	} {
		res, err := api.TraceCall(context.Background(), args, nil, config)
		if err != nil {
			t.Fatalf("test %d: trace failed: %v", i, err)
		}
		want := fmt.Sprintf(`{"%s":true}`, strings.ToLower(precompileAddr.Hex()))
		if have := string(res.(json.RawMessage)); have != want {
			t.Errorf("test %d: have %s, want %s", i, have, want)
		}
	}
	if have := precompile.calls.Load(); have != 2 {
		t.Errorf("custom precompile ran %d times, want 2", have)
	}

	// Without the custom precompile, the address is an ordinary account.
	backend.precompiles = nil
	res, err := api.TraceCall(context.Background(), args, nil, &TraceCallConfig{TraceConfig: TraceConfig{Tracer: &tracer}})
	if err != nil {
		t.Fatalf("trace failed: %v", err)
	}
	want := fmt.Sprintf(`{"%s":false}`, strings.ToLower(precompileAddr.Hex()))
	if have := string(res.(json.RawMessage)); have != want {
		t.Errorf("have %s, want %s", have, want)
	}
}

func TestTraceTransactionPrepareTxFailure(t *testing.T) {
	t.Parallel()

	backend, _ := newMetadataTestBackend(t)
	defer backend.teardown()

	api := NewAPI(&failingPrepareBackend{seiTestBackend: backend})
	tx := backend.chain.GetBlockByNumber(1).Transactions()[0]
	if _, err := api.TraceTransaction(context.Background(), tx.Hash(), nil); err == nil || !strings.Contains(err.Error(), "prepare failed") {
		t.Errorf("want prepare failure, have %v", err)
	}
}

type failingPrepareBackend struct {
	*seiTestBackend
}

func (b *failingPrepareBackend) PrepareTx(vm.StateDB, *types.Transaction) error {
	return errors.New("prepare failed")
}

func TestErrorTrace(t *testing.T) {
	t.Parallel()

	var (
		to      = common.HexToAddress("0x1234")
		tx      = types.NewTx(&types.LegacyTx{To: &to, Gas: 21000})
		msg     = &core.Message{From: common.HexToAddress("0xabcd"), To: &to, GasLimit: 21000, Value: uint256.NewInt(5), Data: []byte{1, 2}}
		txctx   = &Context{BlockHash: common.Hash{1}, BlockNumber: big.NewInt(3), TxIndex: 2, TxHash: tx.Hash()}
		failure = errors.New("boom")
	)
	callTracer, flatCallTracer := "callTracer", "flatCallTracer"

	res, err := errorTrace(failure, tx, msg, txctx, vm.BlockContext{}, &TraceConfig{Tracer: &callTracer})
	if err != nil {
		t.Fatal(err)
	}
	var frame map[string]interface{}
	if err := json.Unmarshal(res.(json.RawMessage), &frame); err != nil {
		t.Fatal(err)
	}
	if frame["error"] != "boom" || frame["type"] != "CALL" || frame["input"] != "0x0102" || frame["value"] != "0x5" || frame["gas"] != "0x5208" {
		t.Errorf("unexpected call trace: %v", frame)
	}

	res, err = errorTrace(failure, tx, msg, txctx, vm.BlockContext{}, &TraceConfig{Tracer: &flatCallTracer})
	if err != nil {
		t.Fatal(err)
	}
	var flat []map[string]interface{}
	if err := json.Unmarshal(res.(json.RawMessage), &flat); err != nil {
		t.Fatal(err)
	}
	if len(flat) != 1 || flat[0]["error"] != "boom" || flat[0]["transactionPosition"] != float64(2) {
		t.Errorf("unexpected flat call trace: %v", flat)
	}

	// Other tracers report the error.
	if _, err := errorTrace(failure, tx, msg, txctx, vm.BlockContext{}, &TraceConfig{}); !errors.Is(err, failure) {
		t.Errorf("want %v, have %v", failure, err)
	}
	// Insufficient funds is not an error for transactions of a block, but is
	// for calls.
	insufficient := fmt.Errorf("%w: address %v", core.ErrInsufficientFunds, msg.From)
	if res, err := errorTrace(insufficient, tx, msg, txctx, vm.BlockContext{}, &TraceConfig{}); err != nil || string(res.(json.RawMessage)) != `{}` {
		t.Errorf("unexpected result for block transaction: %v, %v", res, err)
	}
	if _, err := errorTrace(insufficient, tx, msg, new(Context), vm.BlockContext{}, &TraceConfig{}); !errors.Is(err, core.ErrInsufficientFunds) {
		t.Errorf("want %v, have %v", core.ErrInsufficientFunds, err)
	}
}
