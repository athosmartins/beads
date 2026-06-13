package dolt

import (
	"errors"
	"net"
	"testing"
	"time"
)

// resolveSocketTransport implements a socket-first / TCP-fallback policy for
// Dolt server connections (gt-28itz). These tests stub dialProbe so they run
// without a live Dolt server.

func withStubbedDialProbe(t *testing.T, reachable map[string]bool) {
	t.Helper()
	orig := dialProbe
	t.Cleanup(func() { dialProbe = orig })
	dialProbe = func(network, addr string, _ time.Duration) error {
		if reachable[network+"|"+addr] {
			return nil
		}
		return errors.New("stub: unreachable " + network + " " + addr)
	}
}

// No socket configured → pure TCP, no probing, returns "".
func TestResolveSocketTransport_NoSocket(t *testing.T) {
	withStubbedDialProbe(t, map[string]bool{})
	got := resolveSocketTransport("", "127.0.0.1", 52756, 100*time.Millisecond)
	if got != "" {
		t.Errorf("expected empty socket (TCP), got %q", got)
	}
}

// Socket is live → keep using the socket.
func TestResolveSocketTransport_SocketUp(t *testing.T) {
	withStubbedDialProbe(t, map[string]bool{
		"unix|/tmp/mysql.sock": true,
	})
	got := resolveSocketTransport("/tmp/mysql.sock", "127.0.0.1", 52756, 100*time.Millisecond)
	if got != "/tmp/mysql.sock" {
		t.Errorf("expected socket kept, got %q", got)
	}
}

// Socket down but TCP up → fall back to TCP (return ""). This is the gt-28itz
// regression: /tmp/mysql.sock absent while the server is reachable on :52756.
func TestResolveSocketTransport_SocketDownTCPUp_FallsBack(t *testing.T) {
	withStubbedDialProbe(t, map[string]bool{
		"tcp|" + net.JoinHostPort("127.0.0.1", "52756"): true,
		// unix socket intentionally absent from the reachable set
	})
	got := resolveSocketTransport("/tmp/mysql.sock", "127.0.0.1", 52756, 100*time.Millisecond)
	if got != "" {
		t.Errorf("expected TCP fallback (empty socket), got %q", got)
	}
}

// Socket down AND TCP down → keep the socket so the normal error path reports
// the outage with its socket-specific hint (no silent transport swap).
func TestResolveSocketTransport_BothDown_KeepsSocket(t *testing.T) {
	withStubbedDialProbe(t, map[string]bool{})
	got := resolveSocketTransport("/tmp/mysql.sock", "127.0.0.1", 52756, 100*time.Millisecond)
	if got != "/tmp/mysql.sock" {
		t.Errorf("expected socket kept (both down), got %q", got)
	}
}

// Socket down and no usable TCP port → keep socket (cannot fall back).
func TestResolveSocketTransport_SocketDownNoPort_KeepsSocket(t *testing.T) {
	withStubbedDialProbe(t, map[string]bool{
		"tcp|" + net.JoinHostPort("127.0.0.1", "0"): true, // should never be probed
	})
	got := resolveSocketTransport("/tmp/mysql.sock", "127.0.0.1", 0, 100*time.Millisecond)
	if got != "/tmp/mysql.sock" {
		t.Errorf("expected socket kept (no TCP port), got %q", got)
	}
}
