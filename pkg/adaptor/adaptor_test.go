package adaptor

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/cloudwego/netpoll"
	"github.com/DevNewbie1826/http-over-netpoll/pkg/appcontext"
)

// mockConn embeds netpoll.Connection to satisfy the interface.
// It will panic if any non-overridden method is called.
type mockConn struct {
	netpoll.Connection
	w netpoll.Writer
	r io.Reader // Add reader support
}

func (m *mockConn) Writer() netpoll.Writer {
	return m.w
}

// Implement Read to avoid panic when bufio.NewReader reads from conn
func (m *mockConn) Read(p []byte) (n int, err error) {
	if m.r != nil {
		return m.r.Read(p)
	}
	return 0, io.EOF
}

func (m *mockConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{
		IP:   net.ParseIP("127.0.0.1"),
		Port: 8080,
	}
}

// onlyReader implements only io.Reader, hiding io.WriterTo.
// This forces io.Copy to use ReadFrom instead of WriteTo.
type onlyReader struct {
	io.Reader
}

func TestChunkedEncoding_EndResponse(t *testing.T) {
	// Case 1: Manual Transfer-Encoding: chunked
	var buf bytes.Buffer
	mw := netpoll.NewWriter(&buf)
	mc := &mockConn{w: mw}
	ctx := appcontext.NewRequestContext(mc, context.Background())
	req, _ := http.NewRequest("GET", "/", nil)

	rw := NewResponseWriter(ctx, req)
	rw.Header().Set("Transfer-Encoding", "chunked")
	rw.Write([]byte("hello"))
	err := rw.EndResponse()
	if err != nil {
		t.Fatalf("EndResponse failed: %v", err)
	}

	// We expect the output to contain "0\r\n\r\n" at the end.
	output := buf.String()
	expectedZeroChunk := "0\r\n\r\n"
	if !bytes.Contains(buf.Bytes(), []byte(expectedZeroChunk)) {
		t.Errorf("Expected zero chunk in output, got: %q", output)
	}
}

func TestChunkedEncoding_Flush(t *testing.T) {
	// Case 2: Flush() calling writeHeaders -> Should trigger chunked
	var buf bytes.Buffer
	mw := netpoll.NewWriter(&buf)
	mc := &mockConn{w: mw}
	ctx := appcontext.NewRequestContext(mc, context.Background())
	req, _ := http.NewRequest("GET", "/", nil)

	rw := NewResponseWriter(ctx, req)

	rw.Write([]byte("part1"))
	rw.Flush() // Should trigger chunked headers

	rw.Write([]byte("part2"))
	err := rw.EndResponse() // Should write zero chunk
	if err != nil {
		t.Fatalf("EndResponse failed: %v", err)
	}

	output := buf.String()
	expectedZeroChunk := "0\r\n\r\n"
	if !bytes.Contains(buf.Bytes(), []byte(expectedZeroChunk)) {
		t.Errorf("Expected zero chunk in output, got: %q", output)
	}
	
	if !strings.Contains(output, "Transfer-Encoding: chunked") {
		t.Errorf("Expected Transfer-Encoding: chunked when Flush is called.")
	}
}

func TestNormalResponse(t *testing.T) {
	// Case 3: Normal response -> Now defaults to Chunked (nbio-like behavior)
	var buf bytes.Buffer
	mw := netpoll.NewWriter(&buf)
	mc := &mockConn{w: mw}
	ctx := appcontext.NewRequestContext(mc, context.Background())
	req, _ := http.NewRequest("GET", "/", nil)

	rw := NewResponseWriter(ctx, req)
	rw.Write([]byte("hello"))
	err := rw.EndResponse()
	if err != nil {
		t.Fatalf("EndResponse failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "Transfer-Encoding: chunked") {
		t.Errorf("Expected chunked header in output, got: %q", output)
	}
	// Content-Length should NOT be present
	if strings.Contains(output, "Content-Length:") {
		t.Errorf("Did not expect Content-Length, got: %q", output)
	}
	// Verify chunk format
	if !strings.Contains(output, "\r\n5\r\nhello\r\n0\r\n\r\n") {
		t.Errorf("Body chunk format incorrect. Got: %q", output)
	}
}

func TestReadFrom_WithContentLength(t *testing.T) {
	// If Content-Length is set, should NOT use chunked and treat as fixed length (Zero-Copy path)
	var buf bytes.Buffer
	mw := netpoll.NewWriter(&buf)
	mc := &mockConn{w: mw}
	ctx := appcontext.NewRequestContext(mc, context.Background())
	req, _ := http.NewRequest("GET", "/", nil)

	rw := NewResponseWriter(ctx, req)

	srcData := "0123456789"
	src := &onlyReader{Reader: strings.NewReader(srcData)}

	// Manually set Content-Length
	rw.Header().Set("Content-Length", "10")

	_, err := io.Copy(rw, src)
	if err != nil {
		t.Fatalf("io.Copy failed: %v", err)
	}

	rw.EndResponse()

	output := buf.String()
	t.Logf("Output Headers:\n%s", output)

	if strings.Contains(output, "Transfer-Encoding: chunked") {
		t.Errorf("Should NOT use chunked encoding when Content-Length is set.")
	}
	if !strings.Contains(output, "Content-Length: 10") {
		t.Errorf("Content-Length header missing.")
	}
	// Chunk header (size) should NOT be in body
	if strings.Contains(output, "\r\na\r\n") {
		t.Errorf("Body seems to be chunked (found chunk size 'a').")
	}
}

func TestHijack_Success(t *testing.T) {
	// Case: Hijack before headers written
	var buf bytes.Buffer
	mw := netpoll.NewWriter(&buf)
	mc := &mockConn{w: mw}
	ctx := appcontext.NewRequestContext(mc, context.Background())
	req, _ := http.NewRequest("GET", "/", nil)

	rw := NewResponseWriter(ctx, req)
	conn, bufrw, err := rw.Hijack()
	if err != nil {
		t.Fatalf("Hijack failed: %v", err)
	}
	if conn == nil || bufrw == nil {
		t.Errorf("Hijack returned nil conn or bufio")
	}
	if !rw.Hijacked() {
		t.Errorf("Hijacked() should return true")
	}

	// EndResponse should be safe to call
	rw.EndResponse()
}

func TestHijack_AfterWrite(t *testing.T) {
	// Case: Hijack AFTER headers written -> Should Fail
	var buf bytes.Buffer
	mw := netpoll.NewWriter(&buf)
	mc := &mockConn{w: mw}
	ctx := appcontext.NewRequestContext(mc, context.Background())
	req, _ := http.NewRequest("GET", "/", nil)

	rw := NewResponseWriter(ctx, req)
	rw.Write([]byte("trigger headers"))
	rw.Flush() // Force headers to be written

	_, _, err := rw.Hijack()
	if err == nil {
		t.Errorf("Expected error when hijacking after headers written, got nil")
	}
}
