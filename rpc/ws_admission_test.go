package rpc

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestWSConcurrentRequestBytes(t *testing.T) {
	t.Parallel()

	srv := newTestServer()
	defer srv.Stop()

	p1, p2 := net.Pipe()
	defer p1.Close()
	defer p2.Close()

	payload := fmt.Sprintf(
		`{"jsonrpc":"2.0","id":%d,"method":"test_block","_pad":"%s"}`,
		1, strings.Repeat("x", 48),
	)
	frameSize := int64(len(payload))
	srv.SetReadLimits(frameSize)
	srv.SetWSConcurrentRequestBytes(frameSize)

	go srv.ServeCodec(NewCodec(p1), 0)

	writeReq := func(id int) {
		msg := fmt.Sprintf(
			`{"jsonrpc":"2.0","id":%d,"method":"test_block","_pad":"%s"}`,
			id, strings.Repeat("x", 48),
		)
		if len(msg) != len(payload) {
			t.Fatalf("unexpected payload size %d != %d", len(msg), len(payload))
		}
		if _, err := io.WriteString(p2, msg); err != nil {
			t.Fatalf("write request %d: %v", id, err)
		}
	}

	// One in-flight request exhausts the budget; the next is rejected before dispatch.
	writeReq(1)
	writeReq(2)

	dec := json.NewDecoder(p2)
	deadline := time.After(5 * time.Second)
	gotBusy := false
	for !gotBusy {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for server busy response")
		default:
		}
		var resp jsonrpcMessage
		if err := dec.Decode(&resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if resp.Error != nil && resp.Error.Code == errcodeServerBusy {
			gotBusy = true
		}
	}
}

func TestSetWSConcurrentRequestBytesRaisesToReadLimit(t *testing.T) {
	t.Parallel()

	srv := NewServer()
	srv.SetReadLimits(500)
	srv.SetWSConcurrentRequestBytes(100)
	if srv.wsConcurrentBudget == nil {
		t.Fatal("expected budget to be configured")
	}
	if !srv.wsConcurrentBudget.TryAcquire(500) {
		t.Fatal("budget should allow a single max-size frame")
	}
	srv.wsConcurrentBudget.Release(500)
}

func TestSetReadLimitsRecomputesConcurrentBudget(t *testing.T) {
	t.Parallel()

	srv := NewServer()
	srv.SetWSConcurrentRequestBytes(100)
	srv.SetReadLimits(500)
	if srv.wsConcurrentBudget == nil {
		t.Fatal("expected budget to be configured")
	}
	if !srv.wsConcurrentBudget.TryAcquire(500) {
		t.Fatal("budget should be recomputed when read limit increases")
	}
	srv.wsConcurrentBudget.Release(500)
}
