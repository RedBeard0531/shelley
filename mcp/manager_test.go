package mcp

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// newTestManager returns a Manager whose sessions connect in memory to a
// server with no tools, and counts its connections.
func newTestManager(t *testing.T) (*Manager, *int) {
	srv := sdk.NewServer(&sdk.Implementation{Name: "s"}, nil)
	sdk.AddTool(srv, &sdk.Tool{Name: "t"}, func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, any, error) {
		return &sdk.CallToolResult{}, nil, nil
	})
	m := NewManager(nil, nil)
	t.Cleanup(m.Close)
	connects := 0
	m.transport = func(Server, auth.OAuthHandler) sdk.Transport {
		connects++
		st, ct := sdk.NewInMemoryTransports()
		if _, err := srv.Connect(context.Background(), st, nil); err != nil {
			t.Fatal(err)
		}
		return ct
	}
	return m, &connects
}

func use(t *testing.T, m *Manager, cfg Server) {
	if _, err := m.ListTools(t.Context(), cfg, "c"); err != nil {
		t.Fatal(err)
	}
}

func TestIdleSessionsClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, _ := newTestManager(t)
		sessions := func(wait time.Duration) int {
			time.Sleep(wait)
			synctest.Wait()
			return len(m.Snapshot().Sessions)
		}
		use(t, m, Server{Name: "s"})
		if n := sessions(idleTimeout - time.Second); n != 1 {
			t.Fatalf("%d sessions before the idle timeout", n)
		}
		use(t, m, Server{Name: "s"})
		if n := sessions(idleTimeout - time.Second); n != 1 {
			t.Fatalf("%d sessions after using it again", n)
		}
		if n := sessions(time.Second); n != 0 {
			t.Fatalf("%d sessions after the idle timeout", n)
		}
		if e := m.Snapshot().Events[0]; e.Kind != "session_closed" || e.Message != "idle" {
			t.Fatalf("last event %+v", e)
		}
	})
}

// A request with a server's new configuration doesn't use a session with
// the old one.
func TestReconfiguredSessionReconnects(t *testing.T) {
	m, connects := newTestManager(t)
	use(t, m, Server{Name: "s", URL: "http://a"})
	use(t, m, Server{Name: "s", URL: "http://a", Description: "edited"})
	use(t, m, Server{Name: "s", URL: "http://a", Headers: map[string]string{"K": "v"}})
	if *connects != 2 {
		t.Fatalf("%d connections, want 2", *connects)
	}
}

// A request to a server that forgot its session says so, even when it
// learns only that the session closed: go-sdk fails the connection along
// with the request, and the session closing as a result can cancel the
// request before go-sdk reports why it failed.
func TestForgottenSessionReported(t *testing.T) {
	m, connects := newTestManager(t)
	inMemory := m.transport
	var forgot atomic.Bool
	m.transport = func(cfg Server, h auth.OAuthHandler) sdk.Transport {
		return forgetfulTransport{inMemory(cfg, h), &forgot}
	}
	cfg := Server{Name: "s"}
	use(t, m, cfg)
	forgot.Store(true)
	_, err := m.CallTool(t.Context(), cfg, "c", "t", nil, 0)
	if !errors.Is(err, sdk.ErrSessionMissing) {
		t.Fatalf("forgotten session: %v", err)
	}
	forgot.Store(false)
	use(t, m, cfg)
	if *connects != 2 {
		t.Fatalf("%d connections, want 2", *connects)
	}
}

// forgetfulTransport's connections fail, once forgot is set, as go-sdk's
// Streamable HTTP client does when the server answers 404: the connection
// fails, then the write, and later writes fail at once. That first write
// returns only once its request is canceled, as the session closes, which
// makes the race deterministic.
type forgetfulTransport struct {
	sdk.Transport
	forgot *atomic.Bool
}

func (t forgetfulTransport) Connect(ctx context.Context) (sdk.Connection, error) {
	c, err := t.Transport.Connect(ctx)
	return &forgetfulConn{Connection: c, forgot: t.forgot}, err
}

type forgetfulConn struct {
	sdk.Connection
	forgot *atomic.Bool
	failed atomic.Bool
}

var errForgotten = fmt.Errorf("sending: %w", sdk.ErrSessionMissing)

func (c *forgetfulConn) Read(ctx context.Context) (jsonrpc.Message, error) {
	msg, err := c.Connection.Read(ctx)
	if err != nil && c.forgot.Load() {
		err = errForgotten
	}
	return msg, err
}

func (c *forgetfulConn) Write(ctx context.Context, msg jsonrpc.Message) error {
	if !c.forgot.Load() {
		return c.Connection.Write(ctx, msg)
	}
	if c.failed.CompareAndSwap(false, true) {
		c.Connection.Close() // fails Read
		<-ctx.Done()
	}
	return errForgotten
}
