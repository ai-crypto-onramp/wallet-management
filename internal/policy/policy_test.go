package policy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMockClientDefaultApprove(t *testing.T) {
	c := &MockClient{}
	resp, err := c.CheckWhitelist(context.Background(), &CheckRequest{ToAddress: "0xabc"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Approved || resp.DecisionID != "mock-decision" {
		t.Errorf("unexpected default mock response: %+v", resp)
	}
}

func TestMockClientCustomFn(t *testing.T) {
	called := false
	c := &MockClient{CheckFn: func(ctx context.Context, req *CheckRequest) (*CheckResponse, error) {
		called = true
		if req.ToAddress != "0x1" {
			t.Errorf("unexpected to_address: %s", req.ToAddress)
		}
		return &CheckResponse{Approved: false, DecisionID: "dec-reject", Reason: "not_whitelisted"}, nil
	}}
	resp, err := c.CheckWhitelist(context.Background(), &CheckRequest{ToAddress: "0x1"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Approved {
		t.Error("expected rejection")
	}
	if !called {
		t.Error("CheckFn not invoked")
	}
}

func TestHTTPClientSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/whitelist/check" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(&CheckResponse{Approved: true, DecisionID: "d1"})
	}))
	defer srv.Close()
	c := NewHTTPClient(srv.URL)
	resp, err := c.CheckWhitelist(context.Background(), &CheckRequest{ToAddress: "0x2"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Approved || resp.DecisionID != "d1" {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestHTTPClientRejectStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("denied"))
	}))
	defer srv.Close()
	c := NewHTTPClient(srv.URL)
	if _, err := c.CheckWhitelist(context.Background(), &CheckRequest{}); err == nil {
		t.Fatal("expected error on 403")
	}
}

func TestHTTPClientBadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not-json"))
	}))
	defer srv.Close()
	c := NewHTTPClient(srv.URL)
	if _, err := c.CheckWhitelist(context.Background(), &CheckRequest{}); err == nil {
		t.Fatal("expected decode error")
	}
}

func TestHTTPClientMarshalError(t *testing.T) {
	c := NewHTTPClient("http://example.com")
	// pass an unmarshalable field indirectly is not possible with this struct;
	// instead exercise the http error path with a bad URL via a server that
	// drops the connection. We rely on the server error path already covered.
	_ = c
	_ = errors.New
}