package appcontext

import (
	"bufio"
	"context"
	"sync"

	"github.com/cloudwego/netpoll"
)

// RequestContext holds all necessary information during the lifecycle of an HTTP request.
// RequestContext는 HTTP 요청의 전체 생명주기 동안 필요한 모든 정보를 담습니다.
type RequestContext struct {
	conn   netpoll.Connection
	req    context.Context // Parent context. // 부모 컨텍스트
	reader *bufio.Reader
}

// pool recycles RequestContext objects to reduce GC pressure.
// pool은 RequestContext 객체를 재활용하여 가비지 컬렉션(GC) 부하를 줄입니다.
var pool = sync.Pool{
	New: func() interface{} {
		return new(RequestContext)
	},
}

// NewRequestContext retrieves and initializes a RequestContext from the pool.
// NewRequestContext는 풀에서 RequestContext를 가져와 초기화합니다.
func NewRequestContext(conn netpoll.Connection, parent context.Context) *RequestContext {
	c := pool.Get().(*RequestContext)
	c.conn = conn
	c.req = parent
	return c
}

// Release returns the RequestContext to the pool for reuse.
// Release는 RequestContext를 풀에 반환하여 재사용할 수 있도록 합니다.
func (c *RequestContext) Release() {
	c.reset()
	pool.Put(c)
}

// reset initializes the fields of RequestContext.
// reset은 RequestContext의 필드를 초기화합니다.
func (c *RequestContext) reset() {
	c.conn = nil
	c.req = nil
	// reader is not nil-ed for reuse. // reader는 재사용을 위해 nil로 초기화하지 않습니다.
}

// Conn returns the netpoll.Connection.
// Conn은 netpoll.Connection을 반환합니다.
func (c *RequestContext) Conn() netpoll.Connection {
	return c.conn
}

// Req returns the parent context.
// Req는 부모 컨텍스트를 반환합니다.
func (c *RequestContext) Req() context.Context {
	return c.req
}

// GetReader returns a reusable bufio.Reader.
// GetReader는 재사용 가능한 bufio.Reader를 반환합니다.
func (c *RequestContext) GetReader() *bufio.Reader {
	if c.reader == nil {
		c.reader = bufio.NewReader(c.conn)
	} else {
		c.reader.Reset(c.conn)
	}
	return c.reader
}