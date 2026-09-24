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

package rpc

import (
	"context"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestServerDeadlineHookAppliesToCall checks that a hook installed via
// SetDeadlineHook is consulted for a plain method call, with the full
// "namespace_method" name, and that the context it returns is the one the
// method callback actually runs under.
func TestServerDeadlineHookAppliesToCall(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotMethods []string

	server := newTestServer()
	defer server.Stop()
	server.SetDeadlineHook(func(ctx context.Context, method string) (context.Context, context.CancelFunc) {
		mu.Lock()
		gotMethods = append(gotMethods, method)
		mu.Unlock()
		return context.WithTimeout(ctx, 50*time.Millisecond)
	})
	client := DialInProc(server)
	defer client.Close()

	// Block waits on ctx.Done(); without the hook's deadline it would hang for
	// the life of the test.
	err := client.CallContext(context.Background(), nil, "test_block")
	if err == nil {
		t.Fatal("expected an error from a call bounded by the deadline hook")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(gotMethods) != 1 || gotMethods[0] != "test_block" {
		t.Fatalf("hook saw methods %v, want [test_block]", gotMethods)
	}
}

// TestServerDeadlineHookAppliesToHTTP checks the separate serveSingleRequest
// wiring used by HTTP requests.
func TestServerDeadlineHookAppliesToHTTP(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotMethods []string

	server := newTestServer()
	defer server.Stop()
	server.SetDeadlineHook(func(ctx context.Context, method string) (context.Context, context.CancelFunc) {
		mu.Lock()
		gotMethods = append(gotMethods, method)
		mu.Unlock()
		return context.WithTimeout(ctx, 50*time.Millisecond)
	})
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	client, err := DialHTTP(httpServer.URL)
	if err != nil {
		t.Fatal("can't dial HTTP server:", err)
	}
	defer client.Close()

	err = client.CallContext(context.Background(), nil, "test_block")
	if err == nil {
		t.Fatal("expected an error from a call bounded by the deadline hook")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(gotMethods) != 1 || gotMethods[0] != "test_block" {
		t.Fatalf("hook saw methods %v, want [test_block]", gotMethods)
	}
}

// TestServerDeadlineHookTimeoutErrorShape checks that a method failing after
// the hook's deadline passed reports the same error as the server's own
// request timeout, so clients see one timeout shape regardless of which
// mechanism bounded the call.
func TestServerDeadlineHookTimeoutErrorShape(t *testing.T) {
	t.Parallel()

	server := newTestServer()
	defer server.Stop()
	server.SetDeadlineHook(func(ctx context.Context, method string) (context.Context, context.CancelFunc) {
		return context.WithTimeout(ctx, 50*time.Millisecond)
	})
	client := DialInProc(server)
	defer client.Close()

	err := client.CallContext(context.Background(), nil, "test_block")
	var rpcErr Error
	if !errors.As(err, &rpcErr) {
		t.Fatalf("error %v (%T) is not an rpc.Error", err, err)
	}
	if rpcErr.ErrorCode() != errcodeTimeout {
		t.Errorf("error code is %d, want %d", rpcErr.ErrorCode(), errcodeTimeout)
	}
	if rpcErr.Error() != errMsgTimeout {
		t.Errorf("error message is %q, want %q", rpcErr.Error(), errMsgTimeout)
	}
}

// TestServerDeadlineHookCancelKeepsMethodError checks the other half of that
// contract: a hook that cancels without setting a deadline is not a timeout,
// so the method's own error reaches the client unchanged.
func TestServerDeadlineHookCancelKeepsMethodError(t *testing.T) {
	t.Parallel()

	server := newTestServer()
	defer server.Stop()
	server.SetDeadlineHook(func(ctx context.Context, method string) (context.Context, context.CancelFunc) {
		hookCtx, cancel := context.WithCancel(ctx)
		time.AfterFunc(50*time.Millisecond, cancel)
		return hookCtx, cancel
	})
	client := DialInProc(server)
	defer client.Close()

	err := client.CallContext(context.Background(), nil, "test_block")
	if err == nil {
		t.Fatal("expected an error from a call cancelled by the deadline hook")
	}
	if err.Error() == errMsgTimeout {
		t.Fatal("cancellation without a deadline was reported as a timeout")
	}
}

func TestServerDeadlineHookNilCancel(t *testing.T) {
	t.Parallel()

	server := newTestServer()
	defer server.Stop()
	server.SetDeadlineHook(func(ctx context.Context, method string) (context.Context, context.CancelFunc) {
		return ctx, nil
	})
	client := DialInProc(server)
	defer client.Close()

	var result string
	if err := client.CallContext(context.Background(), &result, "test_rets"); err != nil {
		t.Fatal("call with nil hook CancelFunc failed:", err)
	}
}

// TestServerDeadlineHookAppliesToSubscribe checks that the same hook also
// applies to *_subscribe dispatch, not just plain calls. runMethod cancels the
// hook context as soon as setup returns, so receiving a notification also
// verifies that subscription lifetime is independent from the setup context.
func TestServerDeadlineHookAppliesToSubscribe(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotMethods []string

	server := newTestServer()
	defer server.Stop()
	server.SetDeadlineHook(func(ctx context.Context, method string) (context.Context, context.CancelFunc) {
		mu.Lock()
		gotMethods = append(gotMethods, method)
		mu.Unlock()
		return context.WithCancel(ctx)
	})
	client := DialInProc(server)
	defer client.Close()

	nc := make(chan int)
	sub, err := client.Subscribe(context.Background(), "nftest", nc, "someSubscription", 1, 0)
	if err != nil {
		t.Fatal("can't subscribe:", err)
	}
	defer sub.Unsubscribe()

	select {
	case <-nc:
	case err := <-sub.Err():
		t.Fatal("subscription ended:", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for notification")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(gotMethods) != 1 || gotMethods[0] != "nftest_subscribe" {
		t.Fatalf("hook saw methods %v, want [nftest_subscribe]", gotMethods)
	}
}
