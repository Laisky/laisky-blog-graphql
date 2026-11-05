package web

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	telegramDao "github.com/Laisky/laisky-blog-graphql/internal/web/telegram/dao"
)

// freezingProxy forwards TCP to a MongoDB server until a client message carries
// a trigger, such as the getMore command. From then on it forwards nothing on
// any connection and keeps every connection open, as a stalled network does.
type freezingProxy struct {
	listener net.Listener
	target   string
	trigger  []byte
	frozen   atomic.Bool
	mu       sync.Mutex
	conns    []net.Conn
}

// newFreezingProxy listens on loopback and forwards to target until frozen.
func newFreezingProxy(t *testing.T, target string, trigger []byte) *freezingProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	proxy := &freezingProxy{listener: listener, target: target, trigger: trigger}
	go proxy.serve()
	t.Cleanup(proxy.close)
	return proxy
}

// addr returns the proxy's listening address.
func (p *freezingProxy) addr() string { return p.listener.Addr().String() }

// serve accepts connections until the listener closes.
func (p *freezingProxy) serve() {
	for {
		client, err := p.listener.Accept()
		if err != nil {
			return
		}
		p.track(client)
		go p.handle(client)
	}
}

// track remembers conn so close can end it.
func (p *freezingProxy) track(conn net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.conns = append(p.conns, conn)
}

// handle forwards one client connection, or holds it silently once frozen.
func (p *freezingProxy) handle(client net.Conn) {
	if p.frozen.Load() {
		_, _ = io.Copy(io.Discard, client)
		return
	}
	server, err := net.Dial("tcp", p.target)
	if err != nil {
		_ = client.Close()
		return
	}
	p.track(server)
	go p.pipe(server, client, false)
	p.pipe(client, server, true)
}

// pipe copies src to dst until frozen, then drains src without forwarding. A
// client message carrying the trigger freezes the proxy before it is forwarded.
func (p *freezingProxy) pipe(src, dst net.Conn, fromClient bool) {
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if fromClient && bytes.Contains(buf[:n], p.trigger) {
				p.frozen.Store(true)
			}
			if !p.frozen.Load() {
				if _, werr := dst.Write(buf[:n]); werr != nil {
					return
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// close ends the listener and every connection.
func (p *freezingProxy) close() {
	_ = p.listener.Close()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, conn := range p.conns {
		_ = conn.Close()
	}
}

// TestMongoAlertCredentialCursorCleanupIsBounded reproduces review finding 2
// against MongoDB. LoadAlertCredentials closed its cursor with
// context.WithoutCancel(ctx), which has no deadline. When the network stalled
// mid-read, the read context expired as intended, but the killCursors cleanup
// then blocked until the driver's own connection timeouts. That meant 30 s with
// the service's client options, far past the credential directory's 5 s read.
// Cleanup now has a short deadline of its own, so the call returns soon after
// its context expires.
func TestMongoAlertCredentialCursorCleanupIsBounded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := newAlertMongo(t, ctx)
	// More alert types than the first batch holds (101), so the read needs a
	// getMore and leaves a server cursor open when it is cut short.
	documents := make([]any, 0, 500)
	for i := range 500 {
		documents = append(documents, bson.M{"name": fmt.Sprintf("alert_%03d", i), "push_token": fmt.Sprintf("token-%03d", i)})
	}
	_, err := db.Collection("alert_types").InsertMany(ctx, documents)
	require.NoError(t, err)

	uri, err := url.Parse(os.Getenv("MONGO_QUERY_TEST_URI"))
	require.NoError(t, err)
	proxy := newFreezingProxy(t, uri.Host, []byte("getMore\x00"))
	// The service's own client options (library/db/mongo), through the proxy.
	client, err := mongo.Connect(ctx, options.Client().
		ApplyURI("mongodb://"+proxy.addr()+"/?directConnection=true").
		SetConnectTimeout(30*time.Second).
		SetServerSelectionTimeout(30*time.Second).
		SetSocketTimeout(30*time.Second).
		SetRetryReads(true))
	require.NoError(t, err)
	t.Cleanup(func() {
		disconnectCtx, disconnectCancel := context.WithTimeout(context.Background(), time.Second)
		defer disconnectCancel()
		_ = client.Disconnect(disconnectCtx)
	})
	monitor := telegramDao.NewMonitor(ssoMongoDB{client.Database(db.Name())})

	const readTimeout = time.Second
	readCtx, readCancel := context.WithTimeout(ctx, readTimeout)
	defer readCancel()
	type result struct {
		err     error
		elapsed time.Duration
	}
	done := make(chan result, 1)
	started := time.Now()
	go func() {
		_, _, loadErr := monitor.LoadAlertCredentials(readCtx, 10000)
		done <- result{err: loadErr, elapsed: time.Since(started)}
	}()
	// The bound is the read deadline, plus cleanup's own deadline, plus slack.
	select {
	case got := <-done:
		require.Error(t, got.err, "the stalled read must fail")
		require.True(t, proxy.frozen.Load(), "the read must have reached getMore")
		require.Less(t, got.elapsed, readTimeout+5*time.Second)
	case <-time.After(readTimeout + 8*time.Second):
		t.Fatal("cursor cleanup outlived the read deadline on a stalled connection")
	}
}
