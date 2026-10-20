package main

// The dispatch wires the default transport before any command runs: a hub on this machine is
// reached direct even though a provider plugin's proxy variables rule the environment.

import (
	"io"
	"net/http"
	"reflect"
	"testing"
)

func TestRunSendsALocalHostDirect(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "")
	if code := run([]string{"--version"}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("run --version = %d, want 0", code)
	}
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Fatalf("the default transport is %T, want *http.Transport", http.DefaultTransport)
	}
	// function values compare by their code pointer: the wired function must not be the stock
	// one any more
	if reflect.ValueOf(transport.Proxy).Pointer() == reflect.ValueOf(http.ProxyFromEnvironment).Pointer() {
		t.Fatalf("the default transport still carries the stock proxy function")
	}
	request, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:9443/", nil)
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	proxy, err := transport.Proxy(request)
	if err != nil {
		t.Fatalf("the proxy for loopback: %v", err)
	}
	if proxy != nil {
		t.Fatalf("the proxy for loopback is %s, want direct", proxy)
	}
}
