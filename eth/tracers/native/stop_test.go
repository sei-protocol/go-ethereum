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

package native_test

import (
	"errors"
	"math/big"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/eth/tracers"
	_ "github.com/ethereum/go-ethereum/eth/tracers/native"
	"github.com/ethereum/go-ethereum/params"
	"github.com/stretchr/testify/require"
)

var stoppableTracers = []struct {
	name    string
	prepare func(*tracers.Tracer)
}{
	{name: "4byteTracer"},
	{name: "callTracer", prepare: prepareCallTracer},
	{name: "flatCallTracer", prepare: prepareCallTracer},
	{name: "prestateTracer"},
}

func prepareCallTracer(tracer *tracers.Tracer) {
	tracer.OnEnter(0, byte(vm.CALL), common.Address{}, common.Address{}, nil, 0, big.NewInt(0))
}

func TestTracerStopReason(t *testing.T) {
	for _, test := range stoppableTracers {
		t.Run(test.name, func(t *testing.T) {
			tracer, err := tracers.DefaultDirectory.New(test.name, &tracers.Context{}, nil, params.MainnetChainConfig)
			require.NoError(t, err)
			if test.prepare != nil {
				test.prepare(tracer)
			}

			stopErr := errors.New("stop error")
			stopped := make(chan struct{})
			go func() {
				tracer.Stop(stopErr)
				close(stopped)
			}()
			<-stopped

			_, resultErr := tracer.GetResult()
			require.ErrorIs(t, resultErr, stopErr)
		})
	}
}

// TestTracerStopRace exercises the trace RPC's concurrency pattern: a timeout
// goroutine calls Stop while the handler goroutine obtains the trace result.
func TestTracerStopRace(t *testing.T) {
	for _, test := range stoppableTracers {
		t.Run(test.name, func(t *testing.T) {
			tracer, err := tracers.DefaultDirectory.New(test.name, &tracers.Context{}, nil, params.MainnetChainConfig)
			require.NoError(t, err)
			if test.prepare != nil {
				test.prepare(tracer)
			}

			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				tracer.Stop(errors.New("stop error"))
			}()
			go func() {
				defer wg.Done()
				_, _ = tracer.GetResult()
			}()
			wg.Wait()
		})
	}
}
