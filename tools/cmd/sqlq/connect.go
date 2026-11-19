package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	mssql "github.com/microsoft/go-mssqldb"
	"github.com/microsoft/go-mssqldb/azuread"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/sqlq"
)

// Error categories (spec 2026-11-19-sqlq-fleet-design.md, section 4.3).
const (
	catUsage               = "usage"
	catRefused             = "refused"
	catConnectTransient    = "connect_transient"
	catConnectTimeout      = "connect_timeout"
	catConnectFailed       = "connect_failed"
	catLoginFailed         = "login_failed"
	catDatabaseUnavailable = "database_unavailable"
	catQueryTimeout        = "query_timeout"
	catTransport           = "transport"
	catSQLError            = "sql_error"
	catDeadline            = "deadline"
)

// retryDelay is the wait before a new connection attempt. The field case it
// comes from: an instance reached through ZPA answered 20 s after an EOF.
// A variable so that tests can shorten it.
var retryDelay = 20 * time.Second

// abandonGrace is how long a batch cancelled by the fleet deadline has to
// stop after the attention, before its socket is closed.
var abandonGrace = 5 * time.Second

// tlsCutPrefix starts the message go-mssqldb returns when the connection is
// cut during the TLS handshake. The driver formats the cause with %v, so
// neither errors.Is(io.EOF) nor ECONNRESET can see it (measured on v1.11.0).
const tlsCutPrefix = "TLS Handshake failed: cannot read handshake packet: "

// watchDialer is the Dialer given to the connector. It keeps every socket it
// opens so that a watchdog can close them: neither the context nor the
// profile's connection timeout bounds the PRELOGIN and TLS exchanges of the
// driver (mssql.go: "using the provided context for dialing only"), and
// closing the socket interrupts any read in progress.
type watchDialer struct {
	mu        sync.Mutex
	conns     []net.Conn
	abandoned bool
}

func (d *watchDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	c, err := (&net.Dialer{KeepAlive: 30 * time.Second}).DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.abandoned {
		c.Close()
		return nil, net.ErrClosed
	}
	d.conns = append(d.conns, c)
	return c, nil
}

// HostName makes watchDialer a mssql.HostDialer, so that the driver hands it
// "host:port" instead of resolving the name itself with net.LookupIP, which
// takes no context: the name is then resolved by net.Dialer under the
// attempt's context, and a stuck DNS lookup is bounded like the rest. The
// driver does not call the method itself.
func (d *watchDialer) HostName() string { return "" }

// closeAll closes every socket opened so far; a later attempt may dial again.
func (d *watchDialer) closeAll() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range d.conns {
		c.Close()
	}
	d.conns = nil
}

// abandon closes every socket and refuses any new one.
func (d *watchDialer) abandon() {
	d.mu.Lock()
	d.abandoned = true
	d.mu.Unlock()
	d.closeAll()
}

// openDB builds the database handle on a connector carrying the watchDialer.
func openDB(driver sqlq.Driver, dsn string) (*sql.DB, *watchDialer, error) {
	var (
		c   *mssql.Connector
		err error
	)
	if driver == sqlq.DriverAzureAD {
		c, err = azuread.NewConnector(dsn)
	} else {
		c, err = mssql.NewConnector(dsn)
	}
	if err != nil {
		return nil, nil, err
	}
	d := &watchDialer{}
	c.Dialer = d
	return sql.OpenDB(c), d, nil
}

// errWatchdog stands for a connection that opened just as its attempt timed
// out: it is closed and the attempt counts as timed out.
var errWatchdog = errors.New("the connection attempt timed out")

// openOnce makes one attempt to open the connection, bounded by bound from
// the dial to the end of the login. watchdog reports that the bound was hit.
func openOnce(parent context.Context, db *sql.DB, d *watchDialer, bound time.Duration) (*sql.Conn, bool, error) {
	ctx, cancel := context.WithTimeout(parent, bound)
	defer cancel()
	var fired atomic.Bool
	t := time.AfterFunc(bound, func() {
		fired.Store(true)
		d.closeAll()
	})
	stopParent := context.AfterFunc(parent, d.abandon)
	conn, err := db.Conn(ctx)
	stopped := t.Stop()
	stopParent()
	if err == nil && !stopped {
		// The watchdog closed the socket under a login that had just
		// succeeded. Close alone would return the dead connection to the pool,
		// and the next attempt would get it back without dialing: discard it.
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		conn.Close()
		return nil, true, errWatchdog
	}
	if err != nil {
		d.closeAll()
		return nil, fired.Load(), err
	}
	return conn, false, nil
}

// classify gives the category of an error. opening says it happened while
// the connection was being opened; watchdog that the attempt's bound was
// hit; batch is the batch context, nil while opening. The order is the one of
// the spec: a server error, the watchdog, the batch context, then transport
// and timeout errors by type, then the driver message that carries no type.
func classify(err error, opening, watchdog bool, batch context.Context) string {
	// A server error comes first: it proves the socket was not the failure,
	// even if the watchdog fired while the driver was returning it. A failed
	// login read as a timeout would be retried.
	var me mssql.Error
	if errors.As(err, &me) {
		if _, ok := unavailableCause(me); ok {
			return catDatabaseUnavailable
		}
		if hasErrorNumber(me, errLoginFailed) {
			return catLoginFailed
		}
		if opening {
			return catConnectFailed
		}
		return catSQLError
	}
	if opening && watchdog {
		return catConnectTimeout
	}
	if !opening {
		if batch != nil && batch.Err() != nil {
			return catQueryTimeout
		}
		return catTransport
	}
	var dns *net.DNSError
	switch {
	case errors.As(err, &dns), isAny(err, refusedErrnos):
		return catConnectFailed
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), isAny(err, resetErrnos),
		strings.HasPrefix(err.Error(), tlsCutPrefix):
		return catConnectTransient
	case isTimeout(err):
		return catConnectTimeout
	}
	return catConnectFailed
}

// retryable says whether a connection attempt that failed this way is worth
// another one. Never a server error: a failed login is never retried.
func retryable(category string) bool {
	return category == catConnectTransient || category == catConnectTimeout
}

func hasErrorNumber(e mssql.Error, number int32) bool {
	if e.Number == number {
		return true
	}
	for _, c := range e.All {
		if c.Number == number {
			return true
		}
	}
	return false
}

func isAny(err error, targets []error) bool {
	for _, t := range targets {
		if errors.Is(err, t) {
			return true
		}
	}
	return false
}

func isTimeout(err error) bool {
	if errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// categoryForCode is the category of a failure reported through fail, before
// any connection: the exit code says which.
func categoryForCode(code int) string {
	switch code {
	case exitUsage:
		return catUsage
	case exitRefused:
		return catRefused
	}
	return ""
}
