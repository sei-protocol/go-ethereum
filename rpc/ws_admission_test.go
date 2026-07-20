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

	const (
		sleepDuration = 200 * time.Millisecond
		pad           = 48
	)

	srv := newTestServer()

	p1, p2 := net.Pipe()

	makeMsg := func(id int) string {
		return fmt.Sprintf(
			`{"jsonrpc":"2.0","id":%d,"method":"test_sleep","params":["200ms"],"_pad":"%s"}`,
			id, strings.Repeat("x", pad),
		)
	}
	payload := makeMsg(1)
	frameSize := int64(len(payload))
	srv.SetReadLimits(frameSize)
	srv.SetWSConcurrentRequestBytes(frameSize)

	serveDone := make(chan struct{})
	go func() {
		srv.ServeCodec(NewCodec(p1), 0)
		close(serveDone)
	}()

	t.Cleanup(func() {
		p2.Close()
		p1.Close()
		select {
		case <-serveDone:
		case <-time.After(2 * time.Second):
			t.Error("ServeCodec did not exit within 2s")
		}
		srv.Stop()
	})

	writeReq := func(id int) {
		msg := makeMsg(id)
		if len(msg) != len(payload) {
			t.Fatalf("unexpected payload size %d != %d", len(msg), len(payload))
		}
		if _, err := io.WriteString(p2, msg); err != nil {
			t.Fatalf("write request %d: %v", id, err)
		}
	}

	// One in-flight sleep holds the budget; the next frame is rejected before dispatch.
	writeReq(1)
	writeReq(2)

	dec := json.NewDecoder(p2)
	deadline := time.Now().Add(5 * time.Second)
	gotBusy := false
	for !gotBusy {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for server busy response")
		}
		var resp jsonrpcMessage
		if err := dec.Decode(&resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if resp.Error != nil && resp.Error.Code == errcodeServerBusy {
			gotBusy = true
		}
	}

	// Let the in-flight sleep finish and release its budget before pipe teardown.
	_ = p2.SetReadDeadline(time.Now().Add(sleepDuration + 500*time.Millisecond))
	var sleepResp jsonrpcMessage
	if err := dec.Decode(&sleepResp); err != nil {
		t.Fatalf("decode sleep response: %v", err)
	}
	_ = p2.SetReadDeadline(time.Time{})
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
