package peer

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"testing"
)

// handshake dials the listener with cc, writes one byte, and returns the
// error from the round trip. The server echoes a byte on success. TLS 1.3
// reports client-cert rejection on first read/write, not Dial, so exercise
// both directions.
func handshake(t *testing.T, ln net.Listener, cc *tls.Config) error {
	t.Helper()
	conn, err := tls.Dial("tcp", ln.Addr().String(), cc)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.Write([]byte{1}); err != nil {
		return err
	}
	buf := make([]byte, 1)
	_, err = io.ReadFull(conn, buf)
	return err
}

func TestPinnedHandshake(t *testing.T) {
	serverID, err := EnsureIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	clientID, err := EnsureIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	strangerID, err := EnsureIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	serverFP := IdentityFingerprint(serverID)
	clientFP := IdentityFingerprint(clientID)

	sc := ServerTLSConfig(serverID, func(fp string) bool { return fp == clientFP })
	ln, err := tls.Listen("tcp", "127.0.0.1:0", sc)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 1)
				if _, err := io.ReadFull(c, buf); err == nil {
					c.Write(buf)
				}
			}(c)
		}
	}()

	pin := func(want string) func(string) error {
		return func(fp string) error {
			if fp != want {
				return fmt.Errorf("fingerprint mismatch")
			}
			return nil
		}
	}

	if err := handshake(t, ln, ClientTLSConfig(clientID, pin(serverFP))); err != nil {
		t.Fatalf("pinned peer rejected: %v", err)
	}
	if err := handshake(t, ln, ClientTLSConfig(strangerID, pin(serverFP))); err == nil {
		t.Fatal("server accepted an unpinned client cert")
	}
	if err := handshake(t, ln, ClientTLSConfig(clientID, pin("deadbeef"))); err == nil {
		t.Fatal("client accepted a server cert that fails the pin")
	}
	bare := &tls.Config{InsecureSkipVerify: true} // no client cert at all
	if err := handshake(t, ln, bare); err == nil {
		t.Fatal("server accepted a connection with no client cert")
	}
}
