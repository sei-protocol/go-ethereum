// Copyright 2025 The go-ethereum Authors
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
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestServerSetReadLimits(t *testing.T) {
	t.Parallel()

	// Test different read limits
	testCases := []struct {
		name       string
		readLimit  int64
		testSize   int
		shouldFail bool
	}{
		{
			name:       "small limit with small request - should succeed",
			readLimit:  2048,
			testSize:   500, // Small request data
			shouldFail: false,
		},
		{
			name:       "small limit with large request - should fail",
			readLimit:  2048,
			testSize:   5000, // Large request data that should exceed limit
			shouldFail: true,
		},
		{
			name:       "medium limit with medium request - should succeed",
			readLimit:  10240,
			testSize:   5000, // Medium request data
			shouldFail: false,
		},
		{
			name:       "medium limit with large request - should fail",
			readLimit:  10240,
			testSize:   20000, // Large request data
			shouldFail: true,
		},
		{
			name:       "large limit with large request - should succeed",
			readLimit:  50000,
			testSize:   20000, // Large request data that should fit
			shouldFail: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create server and set read limits
			srv := newTestServer()
			srv.SetReadLimits(tc.readLimit)
			defer srv.Stop()

			// Start HTTP server with WebSocket handler
			httpsrv := httptest.NewServer(srv.WebsocketHandler([]string{"*"}))
			defer httpsrv.Close()

			wsURL := "ws:" + strings.TrimPrefix(httpsrv.URL, "http:")

			// Connect WebSocket client
			client, err := DialOptions(context.Background(), wsURL)
			if err != nil {
				t.Fatalf("can't dial: %v", err)
			}
			defer client.Close()

			// Create large request data - this is what will be limited
			largeString := strings.Repeat("A", tc.testSize)

			// Send the large string as a parameter in the request
			var result echoResult
			err = client.Call(&result, "test_echo", largeString, 42, &echoArgs{S: "test"})

			if tc.shouldFail {
				// Expecting an error due to read limit exceeded
				if err == nil {
					t.Fatalf("expected error for request size %d with limit %d, but got none", tc.testSize, tc.readLimit)
				}
				// Check if it's the expected message size limit error. The server
				// enforces the limit by rejecting the oversized frame and closing the
				// connection; depending on timing, the client either observes gorilla's
				// graceful close(1009, "message too big") on its next read, or a raw
				// TCP reset if the server closes while the client is still writing, or
				// websocket.ErrCloseSent if the client already echoed the server's close
				// frame while its multi-frame write was still in progress.
				msg := err.Error()
				if !strings.Contains(msg, "message too big") && !strings.Contains(msg, "connection reset") &&
					!errors.Is(err, websocket.ErrCloseSent) {
					t.Fatalf("expected 'message too big', 'connection reset' or close-sent error, got: %v", err)
				}
			} else {
				// Expecting success
				if err != nil {
					t.Fatalf("unexpected error for request size %d with limit %d: %v", tc.testSize, tc.readLimit, err)
				}
				// Verify the response is correct - the echo should return our string
				if result.String != largeString {
					t.Fatalf("expected echo result to match input")
				}
			}
		})
	}
}

// Test that SetReadLimits properly updates the server's readerLimit field
func TestServerSetReadLimitsField(t *testing.T) {
	server := NewServer()

	// Test initial default value
	if server.wsReadLimit != wsDefaultReadLimit {
		t.Errorf("expected initial readerLimit to be %d, got %d", wsDefaultReadLimit, server.wsReadLimit)
	}

	// Test setting different values
	testValues := []int64{1024, 10240, 102400, 1048576}

	for _, expectedLimit := range testValues {
		server.SetReadLimits(expectedLimit)
		if server.wsReadLimit != expectedLimit {
			t.Errorf("expected readerLimit to be %d after SetReadLimits, got %d", expectedLimit, server.wsReadLimit)
		}
	}
}

// TestServerRegisterDenyList checks that HTTP requests calling a denied method are
// rejected with method-not-found, while other methods are still served.
func TestServerRegisterDenyList(t *testing.T) {
	t.Parallel()

	srv := newTestServer()
	srv.RegisterDenyList("test_echo")
	defer srv.Stop()
	httpsrv := httptest.NewServer(srv)
	defer httpsrv.Close()

	client, err := DialHTTP(httpsrv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var result echoResult
	err = client.Call(&result, "test_echo", "x", 1)
	var rpcErr Error
	if !errors.As(err, &rpcErr) || rpcErr.ErrorCode() != -32601 {
		t.Fatalf("expected method-not-found error for denied method, got %v", err)
	}
	if err := client.Call(nil, "test_null"); err != nil {
		t.Fatalf("non-denied method failed: %v", err)
	}
}
