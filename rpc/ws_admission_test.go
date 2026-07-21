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

func TestWSConcurrentRequestBytesSingleInFlight(t *testing.T) {
	t.Parallel()

	const pad = 48
	srv := newTestServer()
	p1, p2 := net.Pipe()
	sleepDuration := 50 * time.Millisecond
	makeMsg := func(id int) string {
		return fmt.Sprintf(
			`{"jsonrpc":"2.0","id":%d,"method":"test_sleep","params":[%d],"_pad":"%s"}`,
			id, sleepDuration.Nanoseconds(), strings.Repeat("x", pad),
		)
	}
	payload := makeMsg(1)
	frameSize := int64(len(payload))
	srv.SetReadLimits(frameSize)
	srv.SetWSConcurrentRequestBytes(frameSize)
	go srv.ServeCodec(NewCodec(p1), 0)
	t.Cleanup(func() { p2.Close(); p1.Close(); srv.Stop() })
	if _, err := io.WriteString(p2, payload); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(p2)
	_ = p2.SetReadDeadline(time.Now().Add(time.Second))
	var resp jsonrpcMessage
	if err := dec.Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
}

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
			`{"jsonrpc":"2.0","id":%d,"method":"test_sleep","params":[%d],"_pad":"%s"}`,
			id, sleepDuration.Nanoseconds(), strings.Repeat("x", pad),
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

	// Budget holds through handler work; the second frame is not read until then.
	writeReq(1)
	writeReq(2)

	dec := json.NewDecoder(p2)

	_ = p2.SetReadDeadline(time.Now().Add(sleepDuration + time.Second))
	var firstResp jsonrpcMessage
	if err := dec.Decode(&firstResp); err != nil {
		t.Fatalf("decode first response: %v", err)
	}
	if string(firstResp.ID) != "1" {
		t.Fatalf("expected first response id 1, got %s", string(firstResp.ID))
	}

	_ = p2.SetReadDeadline(time.Now().Add(sleepDuration + time.Second))
	var secondResp jsonrpcMessage
	if err := dec.Decode(&secondResp); err != nil {
		t.Fatalf("decode second response: %v", err)
	}
	if string(secondResp.ID) != "2" {
		t.Fatalf("expected second response id 2, got %s", string(secondResp.ID))
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
