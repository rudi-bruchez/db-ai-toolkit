package main

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/sqlq"
)

// fakeServer listens on 127.0.0.1:0 and runs handle for every connection it
// accepts. It counts the accepts, and stops (closing every connection still
// open) at the end of the test.
type fakeServer struct {
	ln      net.Listener
	accepts atomic.Int32
	mu      sync.Mutex
	conns   []net.Conn
}

func startFake(t *testing.T, handle func(n int, c net.Conn)) *fakeServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeServer{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			n := int(f.accepts.Add(1))
			f.mu.Lock()
			f.conns = append(f.conns, c)
			f.mu.Unlock()
			go handle(n, c)
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, c := range f.conns {
			c.Close()
		}
	})
	return f
}

// profile points at the fake. encrypt "disable" keeps the login in clear,
// "" leaves sqlq's default (true), which starts TLS after PRELOGIN.
func (f *fakeServer) profile(encrypt string) sqlq.Profile {
	port := f.ln.Addr().(*net.TCPAddr).Port
	return sqlq.Profile{Name: "fake", Server: "127.0.0.1," + strconv.Itoa(port), Database: "D",
		Auth: sqlq.AuthSQL, User: "u", PasswordEnv: "X", Mode: "readonly", Encrypt: encrypt,
		TrustServerCertificate: true}
}

func fixedSecret(sqlq.Profile) (string, error) { return "pw", nil }

// readPacket reads one TDS packet and returns its type.
func readPacket(c net.Conn) (byte, error) {
	h := make([]byte, 8)
	if _, err := io.ReadFull(c, h); err != nil {
		return 0, err
	}
	body := make([]byte, int(binary.BigEndian.Uint16(h[2:4]))-8)
	_, err := io.ReadFull(c, body)
	return h[0], err
}

func writePacket(c net.Conn, payload []byte) {
	h := []byte{0x04, 0x01, 0, 0, 0, 0, 1, 0}
	binary.BigEndian.PutUint16(h[2:4], uint16(8+len(payload)))
	c.Write(append(h, payload...))
}

// prelogin is a PRELOGIN response: VERSION, then ENCRYPTION set to enc
// (0 off, 1 on, 2 not supported).
func prelogin(enc byte) []byte {
	return []byte{0x00, 0x00, 0x0B, 0x00, 0x06, 0x01, 0x00, 0x11, 0x00, 0x01, 0xFF,
		0x10, 0x00, 0x00, 0x00, 0x00, 0x00, enc}
}

func ucs2(s string) []byte {
	var b []byte
	for _, r := range utf16.Encode([]rune(s)) {
		b = binary.LittleEndian.AppendUint16(b, r)
	}
	return b
}

// loginFailed is an ERROR token 18456 followed by a DONE with the error bit.
func loginFailed() []byte {
	msg := "Login failed for user 'u'."
	var body []byte
	body = binary.LittleEndian.AppendUint32(body, 18456)
	body = append(body, 1, 14)
	body = binary.LittleEndian.AppendUint16(body, uint16(len(msg)))
	body = append(body, ucs2(msg)...)
	body = append(body, 4)
	body = append(body, ucs2("FAKE")...)
	body = append(body, 0)
	body = binary.LittleEndian.AppendUint32(body, 1)
	tok := []byte{0xAA}
	tok = binary.LittleEndian.AppendUint16(tok, uint16(len(body)))
	tok = append(tok, body...)
	tok = append(tok, 0xFD, 0x02, 0x00, 0x00, 0x00)
	return append(tok, make([]byte, 8)...)
}

var (
	closeOnAccept      = func(_ int, c net.Conn) { c.Close() }
	silent             = func(_ int, c net.Conn) {}
	preloginThenSilent = func(_ int, c net.Conn) {
		if _, err := readPacket(c); err == nil {
			writePacket(c, prelogin(2))
		}
	}
	// dribble answers one byte every 500 ms: a per-read idle timeout never fires.
	dribble = func(_ int, c net.Conn) {
		if _, err := readPacket(c); err != nil {
			return
		}
		h := []byte{0x04, 0x01, 0x10, 0x00}
		for _, b := range h {
			if _, err := c.Write([]byte{b}); err != nil {
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
	// tlsCut asks for encryption, then closes during the handshake.
	tlsCut = func(_ int, c net.Conn) {
		if _, err := readPacket(c); err == nil {
			writePacket(c, prelogin(1))
			readPacket(c)
		}
		c.Close()
	}
	garbage = func(_ int, c net.Conn) {
		readPacket(c)
		c.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
		c.Close()
	}
	rejectLogin = func(_ int, c net.Conn) {
		if _, err := readPacket(c); err != nil {
			return
		}
		writePacket(c, prelogin(2))
		if _, err := readPacket(c); err != nil {
			return
		}
		writePacket(c, loginFailed())
	}
)

func fastOptions(retries int) options {
	return options{maxRows: 50, timeoutSec: 30, connectTimeoutSec: 2, connectRetries: retries}
}

// A server or a tunnel that accepts and then says nothing used to hang sqlq
// for good: the context does not bound PRELOGIN or TLS, and the profile's
// connection timeout is a per-read idle timeout that a dribble never trips.
// Every attempt must end at -connect-timeout.
func TestOpeningIsBoundedByConnectTimeout(t *testing.T) {
	cases := []struct {
		name    string
		handle  func(int, net.Conn)
		encrypt string
		idle    int
	}{
		{"silent", silent, "disable", 0},
		{"prelogin then silent", preloginThenSilent, "disable", 0},
		{"dribble", dribble, "disable", 0},
		{"dribble, profile idle timeout 1 s", dribble, "disable", 1},
		{"silent, TLS", silent, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := startFake(t, tc.handle)
			p := f.profile(tc.encrypt)
			p.ConnectTimeoutSec = tc.idle
			type outcome struct {
				res  sqlq.Result
				code int
			}
			done := make(chan outcome, 1)
			start := time.Now()
			go func() {
				res, code := execute(context.Background(), p, "SELECT 1", nil, fastOptions(0), fixedSecret)
				done <- outcome{res, code}
			}()
			var res sqlq.Result
			var code int
			select {
			case o := <-done:
				res, code = o.res, o.code
			case <-time.After(6 * time.Second):
				t.Fatal("still connecting after 6 s; -connect-timeout is 2 s")
			}
			elapsed := time.Since(start)
			if elapsed > 4*time.Second {
				t.Fatalf("took %v; -connect-timeout is 2 s", elapsed)
			}
			if code != exitConnected || res.Error == nil {
				t.Fatalf("code %d, error %+v; want exit 4", code, res.Error)
			}
			if tc.idle == 0 && elapsed < 2*time.Second {
				t.Errorf("ended after %v, before the bound: %+v", elapsed, res.Error)
			}
			if res.Error.Category != catConnectTimeout {
				t.Errorf("category %q (%s); want connect_timeout", res.Error.Category, res.Error.Message)
			}
			if res.ConnectAttempts != 1 {
				t.Errorf("connect_attempts %d; want 1", res.ConnectAttempts)
			}
		})
	}
}

// Each failure the real driver produces against a fake server, and its category.
func TestOpeningFailuresAreClassified(t *testing.T) {
	refused := func() sqlq.Profile {
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		port := ln.Addr().(*net.TCPAddr).Port
		ln.Close()
		return sqlq.Profile{Name: "fake", Server: "127.0.0.1," + strconv.Itoa(port), Auth: sqlq.AuthSQL,
			User: "u", PasswordEnv: "X", Encrypt: "disable"}
	}
	cases := []struct {
		name    string
		handle  func(int, net.Conn)
		encrypt string
		want    string
	}{
		{"close on accept", closeOnAccept, "disable", catConnectTransient},
		{"cut during TLS", tlsCut, "", catConnectTransient},
		{"garbage", garbage, "disable", catConnectFailed},
		{"login failed", rejectLogin, "disable", catLoginFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := startFake(t, tc.handle)
			res, code := execute(context.Background(), f.profile(tc.encrypt), "SELECT 1", nil, fastOptions(0), fixedSecret)
			if code != exitConnected || res.Error == nil || res.Error.Category != tc.want {
				t.Fatalf("code %d, error %+v; want exit 4 and %s", code, res.Error, tc.want)
			}
		})
	}
	t.Run("refused", func(t *testing.T) {
		res, code := execute(context.Background(), refused(), "SELECT 1", nil, fastOptions(1), fixedSecret)
		if code != exitConnected || res.Error == nil || res.Error.Category != catConnectFailed {
			t.Fatalf("code %d, error %+v; want exit 4 and connect_failed", code, res.Error)
		}
		if res.ConnectAttempts != 1 {
			t.Errorf("a refused connection was retried: %d attempts", res.ConnectAttempts)
		}
	})
}

// A cut, then a silence: one retry, a warning naming it, and the category of
// the last attempt.
func TestTransientFailureIsRetried(t *testing.T) {
	defer func(d time.Duration) { retryDelay = d }(retryDelay)
	retryDelay = 100 * time.Millisecond
	f := startFake(t, func(n int, c net.Conn) {
		if n == 1 {
			c.Close()
		}
	})
	res, code := execute(context.Background(), f.profile("disable"), "SELECT 1", nil, fastOptions(1), fixedSecret)
	if code != exitConnected || res.Error == nil || res.Error.Category != catConnectTimeout {
		t.Fatalf("code %d, error %+v; want exit 4 and connect_timeout", code, res.Error)
	}
	if res.ConnectAttempts != 2 || f.accepts.Load() != 2 {
		t.Errorf("attempts %d, accepts %d; want 2 and 2", res.ConnectAttempts, f.accepts.Load())
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "connect attempt 1 of 2 failed (connect_transient)") {
		t.Errorf("warnings = %q", res.Warnings)
	}
	res, _ = execute(context.Background(), f.profile("disable"), "SELECT 1", nil, fastOptions(0), fixedSecret)
	if res.ConnectAttempts != 1 {
		t.Errorf("-connect-retries 0 made %d attempts", res.ConnectAttempts)
	}
}

// A failed login is never retried, whatever -connect-retries says: repeated
// failures lock accounts and look like credential stuffing.
func TestFailedLoginIsNeverRetried(t *testing.T) {
	defer func(d time.Duration) { retryDelay = d }(retryDelay)
	retryDelay = 10 * time.Millisecond
	f := startFake(t, rejectLogin)
	res, _ := execute(context.Background(), f.profile("disable"), "SELECT 1", nil, fastOptions(3), fixedSecret)
	if res.Error == nil || res.Error.Number != errLoginFailed {
		t.Fatalf("error %+v; want 18456", res.Error)
	}
	if got := f.accepts.Load(); got != 1 || res.ConnectAttempts != 1 {
		t.Errorf("accepts %d, attempts %d; want 1 and 1", got, res.ConnectAttempts)
	}
}

// After the connection opened, an expired batch context means a query
// timeout whatever the driver returned: a timeout during the preamble comes
// back as an untyped "Invalid TDS stream" error.
func TestBatchErrorsAreClassified(t *testing.T) {
	expired, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	untyped := errors.New("Invalid TDS stream: did not get cancellation confirmation from the server " +
		"(current response: context deadline exceeded)")
	if got := classify(untyped, false, false, expired); got != catQueryTimeout {
		t.Errorf("expired batch: %q; want query_timeout", got)
	}
	if got := classify(io.EOF, false, false, context.Background()); got != catTransport {
		t.Errorf("EOF during the batch: %q; want transport", got)
	}
	parent, stop := context.WithCancel(context.Background())
	stop()
	if got := batchCategory(untyped, expired, parent); got != catDeadline {
		t.Errorf("parent gone: %q; want deadline", got)
	}
}
