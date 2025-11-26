package adaptor

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/cloudwego/netpoll"

	"github.com/DevNewbie1826/http-over-netpoll/pkg/appcontext"
	"github.com/DevNewbie1826/http-over-netpoll/pkg/bytebufferpool"
)

var errHijacked = errors.New("connection has been hijacked")

// ResponseWriter implements http.ResponseWriter and wraps netpoll connection.
// ResponseWriter는 http.ResponseWriter 인터페이스를 구현하며 netpoll 연결을 래핑합니다.
type ResponseWriter struct {
	ctx         *appcontext.RequestContext
	req         *http.Request
	header      http.Header
	statusCode  int
	wroteHeader bool
	hijacked    bool
	chunked     bool
	body        *bytebufferpool.ByteBuffer
}

// rwPool recycles ResponseWriter objects to reduce GC pressure.
// rwPool은 ResponseWriter 객체를 재활용하여 가비지 컬렉션(GC) 부하를 줄입니다.
var rwPool = sync.Pool{
	New: func() any {
		return &ResponseWriter{
			header: make(http.Header),
		}
	},
}

// copyBufPool provides buffers for io.CopyBuffer to enable Zero-Alloc copying.
// copyBufPool은 io.CopyBuffer를 위한 버퍼를 제공하여 Zero-Alloc 복사를 가능하게 합니다.
var copyBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 32*1024) // 32KB buffer
		return &b
	},
}

// NewResponseWriter creates a new ResponseWriter from the pool.
// NewResponseWriter는 풀에서 새로운 ResponseWriter를 생성합니다.
func NewResponseWriter(ctx *appcontext.RequestContext, req *http.Request) *ResponseWriter {
	rw := rwPool.Get().(*ResponseWriter)
	rw.ctx = ctx
	rw.req = req
	rw.statusCode = 0
	rw.wroteHeader = false
	rw.hijacked = false
	rw.chunked = false
	rw.body = bytebufferpool.Get()

	// No need to re-allocate header map; it is cleared in Release().
	// 헤더 맵을 다시 할당할 필요가 없습니다. Release()에서 초기화됩니다.

	return rw
}

// Release returns the ResponseWriter to the pool.
// Release는 ResponseWriter를 풀에 반환합니다.
func (rw *ResponseWriter) Release() {
	rw.ctx = nil
	rw.req = nil
	if rw.body != nil {
		bytebufferpool.Put(rw.body)
		rw.body = nil
	}

	// Clear the header map for reuse, avoiding re-allocation overhead.
	// 재사용을 위해 헤더 맵을 초기화하여 재할당 오버헤드를 방지합니다.
	clear(rw.header)

	rwPool.Put(rw)
}

func (rw *ResponseWriter) Header() http.Header {
	return rw.header
}

func (rw *ResponseWriter) WriteHeader(statusCode int) {
	if rw.wroteHeader || rw.hijacked {
		return
	}
	rw.statusCode = statusCode
}

func (rw *ResponseWriter) Write(p []byte) (int, error) {
	if rw.hijacked {
		return 0, errHijacked
	}
	if !rw.wroteHeader {
		if rw.statusCode == 0 {
			rw.statusCode = http.StatusOK
		}
	}
	return rw.body.Write(p)
}

// ReadFrom implements io.ReaderFrom for efficient file transfer.
// This attempts to leverage zero-copy (sendfile) via io.ReaderFrom if the underlying netpoll.Writer supports it.
// As of netpoll v0.7.2, the Writer does not implement io.ReaderFrom, so it falls back to io.CopyBuffer with a pooled buffer.
// ReadFrom은 효율적인 파일 전송을 위해 io.ReaderFrom을 구현합니다.
// 이는 기반 netpoll.Writer가 지원하는 경우 io.ReaderFrom을 통해 제로-카피(sendfile)를 시도합니다.
// netpoll v0.7.2 기준으로 Writer는 io.ReaderFrom을 구현하지 않으므로, 풀링된 버퍼를 사용하는 io.CopyBuffer로 대체됩니다.
func (rw *ResponseWriter) ReadFrom(r io.Reader) (n int64, err error) {
	if rw.hijacked {
		return 0, errHijacked
	}

	writer := rw.ctx.Conn().Writer()

	if !rw.wroteHeader {
		if rw.statusCode == 0 {
			rw.statusCode = http.StatusOK
		}

		// Check if Content-Length is explicitly set or Transfer-Encoding is explicitly chunked
		_, hasCL := rw.header["Content-Length"]
		hasChunked := rw.header.Get("Transfer-Encoding") == "chunked"
		shouldChunk := !hasCL && !hasChunked
		if hasChunked {
			shouldChunk = true
		}

		rw.writeHeaders(writer, shouldChunk)

		// Must flush headers before attempting to send file data
		// 파일 데이터를 전송하기 전에 반드시 헤더를 플러시해야 합니다.
		if err := writer.Flush(); err != nil {
			return 0, err
		}
	}

	// If chunked, we must manually chunk the data from reader.
	// Zero-Copy (ReaderFrom) cannot be easily used with chunked encoding because we need to insert length headers.
	if rw.chunked {
		bufp := copyBufPool.Get().(*[]byte)
		buf := *bufp
		defer copyBufPool.Put(bufp)

		for {
			nr, er := r.Read(buf)
			if nr > 0 {
				// Write chunk header: "Size\r\n"
				chunkHeader := strconv.FormatInt(int64(nr), 16) + "\r\n"
				writer.WriteString(chunkHeader)
				writer.WriteBinary(buf[:nr])
				writer.WriteString("\r\n")
				// Flush immediately to send chunk and prevent buffering huge files in memory.
				if err := writer.Flush(); err != nil {
					return n, err
				}
			}
			if er != nil {
				if er != io.EOF {
					err = er
				}
				break
			}
		}
		// Flush after writing all chunks (except zero chunk, which EndResponse handles)
		if fErr := writer.Flush(); fErr != nil && err == nil {
			err = fErr
		}
		return n, err
	}

	// Attempt to use io.ReaderFrom (sendfile-like optimization)
	// io.ReaderFrom (sendfile과 유사한 최적화) 사용 시도
	if rf, ok := writer.(io.ReaderFrom); ok {
		n, err = rf.ReadFrom(r)
	} else {
		// Fallback to io.CopyBuffer with pooled buffer if io.ReaderFrom is not supported.
		// We use a sync.Pool to minimize memory allocation overhead (Zero-Alloc).
		// io.ReaderFrom이 지원되지 않는 경우 풀링된 버퍼를 사용하는 io.CopyBuffer로 대체합니다.
		// 메모리 할당 오버헤드(Zero-Alloc)를 최소화하기 위해 sync.Pool을 사용합니다.
		bufp := copyBufPool.Get().(*[]byte)
		buf := *bufp
		n, err = io.CopyBuffer(netpollWriterWrapper{w: writer}, r, buf)
		copyBufPool.Put(bufp)
		// Note: netpollWriterWrapper.Write handles flushing to ensure data integrity.
		// 참고: netpollWriterWrapper.Write는 데이터 무결성을 보장하기 위해 플러시를 처리합니다.
	}

	return n, err
}

func (rw *ResponseWriter) Flush() {
	if rw.hijacked {
		return
	}
	writer := rw.ctx.Conn().Writer()

	if !rw.wroteHeader {
		if rw.statusCode == 0 {
			rw.statusCode = http.StatusOK
		}
		rw.writeHeaders(writer, true)
	}

	if rw.body.Len() > 0 {
		chunkHeader := strconv.FormatInt(int64(rw.body.Len()), 16) + "\r\n"
		writer.WriteString(chunkHeader)
		writer.WriteBinary(rw.body.Bytes())
		writer.WriteString("\r\n")
		rw.body.Reset()
	}
	writer.Flush()
}

func (rw *ResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if rw.hijacked {
		return nil, nil, errHijacked
	}
	rw.hijacked = true
	conn := rw.ctx.Conn()
	return conn, bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn)), nil
}

// Hijacked returns true if the connection has been hijacked.
// Hijacked는 연결이 하이재킹되었는지 여부를 반환합니다.
func (rw *ResponseWriter) Hijacked() bool {
	return rw.hijacked
}

func (rw *ResponseWriter) writeHeaders(writer netpoll.Writer, isStreaming bool) {
	if rw.wroteHeader {
		return
	}
	rw.wroteHeader = true

	var buf bytes.Buffer

	if rw.header.Get("Date") == "" {
		rw.header.Set("Date", time.Now().UTC().Format(http.TimeFormat))
	}

	if rw.header.Get("Content-Type") == "" {
		if rw.body.Len() > 0 {
			sniffBuf := rw.body.Bytes()
			if len(sniffBuf) > 512 {
				sniffBuf = sniffBuf[:512]
			}
			rw.header.Set("Content-Type", http.DetectContentType(sniffBuf))
		}
	}

	// Status Line
	// 상태 라인
	buf.WriteString(rw.req.Proto)
	buf.WriteString(" ")
	buf.WriteString(strconv.Itoa(rw.statusCode))
	buf.WriteString(" ")
	buf.WriteString(http.StatusText(rw.statusCode))
	buf.WriteString("\r\n")

	// RFC 7230: Status codes 1xx, 204, and 304 MUST NOT have a response body.
	noBody := rw.statusCode >= 100 && rw.statusCode < 200 || rw.statusCode == 204 || rw.statusCode == 304

	if isStreaming && !noBody {
		rw.chunked = true
		buf.WriteString("Transfer-Encoding: chunked\r\n")
		rw.header.Del("Content-Length")
		rw.header.Del("Transfer-Encoding") // Avoid duplication if already set
		// 이미 설정되어 있다면 중복을 피하기 위해 Transfer-Encoding을 삭제합니다.
	} else {
		// Only set Content-Length if not already set (e.g. by ServeFile)
		// and if body exists.
		// Content-Length가 아직 설정되지 않았고 (예: ServeFile에 의해), 본문이 존재하는 경우에만 설정합니다.
		if rw.header.Get("Content-Length") == "" {
			// For 304, we also skip adding Content-Length if not set, to avoid overwriting cache metadata with 0.
			if !noBody {
				rw.header.Set("Content-Length", strconv.Itoa(rw.body.Len()))
			}
		}
		rw.header.Del("Transfer-Encoding")
	}

	// Headers
	// 헤더
	for k, v := range rw.header {
		for _, vv := range v {
			buf.WriteString(k)
			buf.WriteString(": ")
			buf.WriteString(vv)
			buf.WriteString("\r\n")
		}
	}
	buf.WriteString("\r\n")
	writer.WriteBinary(buf.Bytes())
}

func (rw *ResponseWriter) EndResponse() error {
	if rw.hijacked {
		bytebufferpool.Put(rw.body)
		return nil
	}

	writer := rw.ctx.Conn().Writer()
	isStreaming := rw.header.Get("Transfer-Encoding") == "chunked"

	if !rw.wroteHeader {
		if rw.statusCode == 0 {
			rw.statusCode = http.StatusOK
		}
		rw.writeHeaders(writer, isStreaming)
	}

	// RFC 7230: Status codes 1xx, 204, and 304 MUST NOT have a response body.
	noBody := rw.statusCode >= 100 && rw.statusCode < 200 || rw.statusCode == 204 || rw.statusCode == 304

	if rw.chunked {
		// Note: Using chunked encoding with 204/304 is generally invalid, but if the user
		// triggered it via Flush(), headers are already sent. We should at least
		// prevent sending body data if noBody is true.
		if !noBody && rw.body.Len() > 0 {
			chunkHeader := strconv.FormatInt(int64(rw.body.Len()), 16) + "\r\n"
			writer.WriteString(chunkHeader)
			writer.WriteBinary(rw.body.Bytes())
			writer.WriteString("\r\n")
		}
		// Zero chunk must be sent to terminate chunked stream, even for noBody?
		// Technically 204 should not have Transfer-Encoding either.
		// But if we are here, headers are likely already sent with Transfer-Encoding.
		// Sending zero chunk is safer to keep connection in sync than hanging.
		writer.WriteString("0\r\n\r\n")
	} else {
		// Fixed Length or No Body
		if !noBody && rw.body.Len() > 0 {
			if _, err := writer.WriteBinary(rw.body.Bytes()); err != nil {
				bytebufferpool.Put(rw.body)
				return err
			}
		}
	}

	bytebufferpool.Put(rw.body)
	rw.body = nil
	return writer.Flush()
}

func GetRequest(ctx *appcontext.RequestContext) (*http.Request, error) {
	reader := ctx.GetReader()
	req, err := http.ReadRequest(reader)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, io.EOF
		}
		return nil, errors.New("failed to read request")
	}
	req.URL.Scheme = "http"
	req.URL.Host = req.Host
	req.RemoteAddr = ctx.Conn().RemoteAddr().String()

	return req, nil
}

// netpollWriterWrapper adapts netpoll.Writer to io.Writer.
// netpollWriterWrapper는 netpoll.Writer를 io.Writer에 맞게 조정합니다.
type netpollWriterWrapper struct {
	w netpoll.Writer
}

// Write writes data to the underlying netpoll writer and flushes it immediately.
// Immediate flushing is required to prevent data corruption or truncation in netpoll's internal buffer,
// ensuring data integrity at the cost of slightly higher syscall overhead.
// Write는 데이터를 기반 netpoll Writer에 쓰고 즉시 플러시합니다.
// 즉시 플러싱은 netpoll 내부 버퍼에서 데이터 손상이나 잘림을 방지하기 위해 필요하며,
// 약간 더 높은 시스템 호출 오버헤드를 감수하고 데이터 무결성을 보장합니다.
func (w netpollWriterWrapper) Write(p []byte) (int, error) {
	n, err := w.w.WriteBinary(p)
	if err != nil {
		return n, err
	}
	return n, w.w.Flush()
}
