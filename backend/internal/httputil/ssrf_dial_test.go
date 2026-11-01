package httputil

import (
	"errors"
	"net"
	"testing"
	"time"
)

func listenLoopback(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	return ln.Addr().String()
}

func TestSafeDialerBlocksLoopback(t *testing.T) {
	SetAllowPrivateBaseURL(false)
	addr := listenLoopback(t)
	_, err := SafeDialer(2*time.Second).Dial("tcp", addr)
	var blocked *ErrSSRFBlocked
	if !errors.As(err, &blocked) {
		t.Fatalf("err = %v, want ErrSSRFBlocked", err)
	}
}

func TestSafeDialerAllowsLoopbackWhenRelaxed(t *testing.T) {
	SetAllowPrivateBaseURL(true)
	t.Cleanup(func() { SetAllowPrivateBaseURL(false) })
	addr := listenLoopback(t)
	c, err := SafeDialer(2*time.Second).Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c.Close()
}

func TestValidateHostRejectsPrivate(t *testing.T) {
	SetAllowPrivateBaseURL(false)
	for _, h := range []string{"10.0.0.5", "localhost", "169.254.169.254", "metadata.google.internal", ""} {
		if err := ValidateHost(h); err == nil {
			t.Errorf("ValidateHost(%q) = nil, want error", h)
		}
	}
	if err := ValidateHost("8.8.8.8"); err != nil {
		t.Errorf("ValidateHost(public) = %v", err)
	}
}
