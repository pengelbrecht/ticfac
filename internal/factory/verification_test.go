package factory

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/factory/credentials"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func verificationResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d test response", status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestVerifyEndpointRetriesCloudflareEdgeError1042(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch calls.Add(1) {
		case 1:
			return verificationResponse(1042, "Error 1042: propagation in progress"), nil
		case 2:
			return verificationResponse(http.StatusOK,
				`{"bindings":{"sandboxes":true},"auth":{"configured":true}}`), nil
		default:
			return verificationResponse(http.StatusNotFound, `{"error":"not found"}`), nil
		}
	})}

	opts := Options{
		HTTPClient:     client,
		verifyAttempts: 2,
		verifyDelay:    time.Millisecond,
	}
	if err := verifyEndpoint(context.Background(), opts, "https://factory.example.com", "tkf_test"); err != nil {
		t.Fatalf("verifyEndpoint: %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("HTTP calls = %d, want health, retry health, authenticated probe", got)
	}
}

func TestVerifyEndpointExhaustionRemainsActionable(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		return verificationResponse(http.StatusServiceUnavailable, "secret is still propagating"), nil
	})}

	opts := Options{
		HTTPClient:     client,
		verifyAttempts: 3,
		verifyDelay:    time.Millisecond,
	}
	err := verifyEndpoint(context.Background(), opts, "https://factory.example.com", "tkf_test")
	if err == nil {
		t.Fatal("verifyEndpoint succeeded after exhausting transient failures")
	}
	if !strings.Contains(err.Error(), "nothing is lost by running it again") {
		t.Errorf("error lost the re-run guidance: %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("HTTP calls = %d, want exactly the configured attempts", got)
	}
}

// "no route to host" is the network, however the probe that met it labelled
// it: a deploy's verification keeps probing through it.
func TestVerifyEndpointRetriesAnUnreachableNetwork(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch calls.Add(1) {
		case 1:
			return nil, &net.OpError{Op: "dial", Net: "tcp",
				Err: os.NewSyscallError("connect", syscall.EHOSTUNREACH)}
		case 2:
			return verificationResponse(http.StatusOK,
				`{"bindings":{"sandboxes":true},"auth":{"configured":true}}`), nil
		default:
			return verificationResponse(http.StatusNotFound, `{"error":"not found"}`), nil
		}
	})}
	opts := Options{HTTPClient: client, verifyAttempts: 2, verifyDelay: time.Millisecond}
	if err := verifyEndpoint(context.Background(), opts, "https://factory.example.com", "tkf_test"); err != nil {
		t.Fatalf("verifyEndpoint: %v", err)
	}
	unreachable := verificationError(fmt.Errorf("probe: %w", &net.OpError{Op: "dial", Net: "tcp",
		Err: os.NewSyscallError("connect", syscall.ENETUNREACH)}), false)
	if !isRetryableVerificationError(unreachable) {
		t.Error("an unreachable network was classified as a final answer")
	}
}

func TestVerificationBackoffIsBounded(t *testing.T) {
	want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second}
	for retryNumber, expected := range want {
		if got := verificationBackoff(defaultVerifyDelay, retryNumber+1); got != expected {
			t.Errorf("retry %d delay = %s, want %s", retryNumber+1, got, expected)
		}
	}
}

// recordedFactory is a config naming a factory, as a setup re-run finds it.
func recordedFactory(t *testing.T) *credentials.File {
	t.Helper()
	cfg, err := credentials.LoadFrom(filepath.Join(t.TempDir(), credentials.FileName))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Set(credentials.KeyURL, "https://factory.example.com")
	cfg.Set(credentials.KeyToken, "tkf_test")
	return cfg
}

// Setup's probe of a factory it already has on record rides out a network
// blip ("no route to host") instead of telling the operator to redeploy a
// factory that is fine — and gives up within its bound when the blip does not
// clear, still naming the redeploy.
func TestSetupRetriesAnUnreachableRecordedFactoryBriefly(t *testing.T) {
	unreachable := func() error {
		return &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.EHOSTUNREACH)}
	}
	for _, tc := range []struct {
		name      string
		failFirst int32
		wantErr   bool
		wantCalls int32
	}{
		{"a blip clears", 1, false, 3}, // health fails, health, authenticated probe
		{"it does not clear", 100, true, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				n := calls.Add(1)
				if n <= tc.failFirst {
					return nil, unreachable()
				}
				if req.URL.Path == "/health" {
					return verificationResponse(http.StatusOK,
						`{"bindings":{"sandboxes":true},"auth":{"configured":true}}`), nil
				}
				return verificationResponse(http.StatusNotFound, `{"error":"not found"}`), nil
			})}
			cfg := recordedFactory(t)
			opts := SetupOptions{verifyDelay: time.Millisecond}
			result := &SetupResult{}
			err := setupDeployment(context.Background(), bufio.NewReader(strings.NewReader("")), io.Discard,
				client, &cfg, opts, t.TempDir(), result)
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "did not answer") {
					t.Fatalf("got %v, want the recorded factory reported unanswering", err)
				}
			} else if err != nil {
				t.Fatalf("setupDeployment: %v", err)
			}
			if got := calls.Load(); got != tc.wantCalls {
				t.Errorf("HTTP calls = %d, want %d", got, tc.wantCalls)
			}
		})
	}
}
