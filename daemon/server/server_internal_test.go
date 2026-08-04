package server

import (
	"fmt"
	"net"
	"testing"
)

func TestIsBenignHandshakeClose(t *testing.T) {
	if !isBenignHandshakeClose(fmt.Errorf("failed to get reader: %w", net.ErrClosed)) {
		t.Fatal("wrapped net.ErrClosed should be treated as a client disconnect")
	}
	if !isBenignHandshakeClose(fmt.Errorf("failed to get reader: use of closed network connection")) {
		t.Fatal("closed network connection text should be treated as a client disconnect")
	}
	if isBenignHandshakeClose(fmt.Errorf("authorization failed")) {
		t.Fatal("auth failures must remain handshake failures")
	}
}
