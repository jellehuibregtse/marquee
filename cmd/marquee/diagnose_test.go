package main

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestListenErrorMessageNamesHolder(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	addr := ln.Addr().String()

	_, err = net.Listen("tcp", addr)
	if err == nil {
		t.Fatalf("second Listen on %s succeeded, expected address-in-use", addr)
	}

	msg := listenErrorMessage(addr, err)
	if !strings.Contains(msg, addr) {
		t.Errorf("message %q does not name the address %s", msg, addr)
	}
	if !strings.Contains(msg, "already in use") {
		t.Errorf("message %q does not say the address is in use", msg)
	}
	if !strings.Contains(msg, "--listen") {
		t.Errorf("message %q does not suggest --listen", msg)
	}

	if _, lookErr := exec.LookPath("lsof"); lookErr != nil {
		t.Skipf("lsof not available, PID naming degrades by design: %v", lookErr)
	}
	if want := "PID " + strconv.Itoa(os.Getpid()); !strings.Contains(msg, want) {
		t.Errorf("message %q does not contain %q (we hold the port)", msg, want)
	}
}

func TestListenErrorMessageWithoutHolderStillFriendly(t *testing.T) {
	err := &net.OpError{Op: "listen", Err: errors.New("permission denied")}
	msg := listenErrorMessage("127.0.0.1:80", err)
	if !strings.Contains(msg, "could not listen on 127.0.0.1:80") {
		t.Errorf("non-EADDRINUSE error got %q, want the generic listen failure", msg)
	}
	if strings.Contains(msg, "already in use") {
		t.Errorf("non-EADDRINUSE error got the in-use message: %q", msg)
	}
}
