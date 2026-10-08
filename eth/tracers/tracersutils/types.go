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

// Package tracersutils holds types shared between the tracing API and the
// backends that serve it.
package tracersutils

import "github.com/ethereum/go-ethereum/core/vm"

// TraceBlockMetadata describes one entry of a block replayed by the tracing
// API. Chains such as Sei interleave EVM transactions with other transactions
// that also change state; the metadata lists every entry in execution order so
// the non-EVM ones can be replayed without being traced.
type TraceBlockMetadata struct {
	ShouldIncludeInTraceResult bool
	// must be set if ShouldIncludeInTraceResult
	IdxInEthBlock int
	// must be set if !ShouldIncludeInTraceResult
	TraceRunnable func(vm.StateDB)
}
