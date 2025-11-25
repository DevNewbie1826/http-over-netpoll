package server

import (
	"context"
	"log"
	"time"

	"github.com/DevNewbie1826/http-over-netpoll/pkg/engine"

	"github.com/cloudwego/netpoll"
	"github.com/valyala/fasthttp/reuseport"
)

// Server is the top-level structure for the netpoll server.
// Server는 netpoll 서버의 최상위 구조체입니다.
type Server struct {
	Engine           *engine.Engine
	eventLoop        netpoll.EventLoop
	keepAliveTimeout time.Duration
	readTimeout      time.Duration
	writeTimeout     time.Duration
}

// Option is a function type for configuring the Server.
// Option은 서버 설정을 위한 함수 타입입니다.
type Option func(*Server)

// WithReadTimeout sets the read timeout.
// WithReadTimeout은 읽기 타임아웃을 설정합니다.
func WithReadTimeout(d time.Duration) Option {
	return func(s *Server) {
		s.readTimeout = d
	}
}

// WithWriteTimeout sets the write timeout.
// WithWriteTimeout은 쓰기 타임아웃을 설정합니다.
func WithWriteTimeout(d time.Duration) Option {
	return func(s *Server) {
		s.writeTimeout = d
	}
}

// WithKeepAliveTimeout sets the Keep-Alive timeout.
// WithKeepAliveTimeout은 Keep-Alive 타임아웃을 설정합니다.
func WithKeepAliveTimeout(d time.Duration) Option {
	return func(s *Server) {
		s.keepAliveTimeout = d
	}
}

// NewServer creates a new Server.
// NewServer는 새로운 Server를 생성합니다.
func NewServer(e *engine.Engine, opts ...Option) *Server {
	s := &Server{
		Engine:           e,
		keepAliveTimeout: 30 * time.Second, // Default // 기본값
		readTimeout:      10 * time.Second,
		writeTimeout:     10 * time.Second,
	}

	for _, opt := range opts {
		opt(s)
	}

	return s
}

// Serve starts the netpoll event loop to handle incoming requests.
// It uses SO_REUSEPORT for improved multi-process/thread binding performance.
// Serve는 netpoll 이벤트 루프를 시작하여 들어오는 요청을 처리합니다.
// SO_REUSEPORT를 사용하여 다중 프로세스/스레드 바인딩 성능을 높입니다.
func (s *Server) Serve(addr string) error {
	l, err := reuseport.Listen("tcp", addr)
	if err != nil {
		return err
	}

	listener, err := netpoll.ConvertListener(l)
	if err != nil {
		return err
	}

	log.Printf("Server listening on %s", addr)

	opts := []netpoll.Option{
		netpoll.WithIdleTimeout(s.keepAliveTimeout),
		netpoll.WithOnPrepare(func(conn netpoll.Connection) context.Context {
			conn.SetReadTimeout(s.readTimeout) // nolint:errcheck
			if s.writeTimeout > 0 {
				conn.SetWriteTimeout(s.writeTimeout)
			}

			ctx := context.Background()
			ctx = cancelContext(ctx) // Creates and registers a cancellable context. // 취소 가능한 컨텍스트 생성 및 등록
			return ctx
		}),
		netpoll.WithOnDisconnect(func(ctx context.Context, connection netpoll.Connection) {
			cancelFunc, ok := ctx.Value(ctxCancelKey).(context.CancelFunc)
			if cancelFunc != nil && ok {
				cancelFunc() // Cancels context on connection disconnect. // 연결 종료 시 컨텍스트 취소
			}
		}),
	}

	// OnRequest callback invokes the Engine's ServeConn method.
	// OnRequest 콜백은 Engine의 ServeConn 메서드를 호출합니다.
	eventLoop, err := netpoll.NewEventLoop(s.Engine.ServeConn, opts...)
	if err != nil {
		return err
	}
	s.eventLoop = eventLoop

	return eventLoop.Serve(listener)
}

// Shutdown gracefully shuts down the server.
// Shutdown은 서버를 우아하게 종료합니다.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.eventLoop == nil {
		return nil
	}
	return s.eventLoop.Shutdown(ctx)
}

type ctxCancelKeyStruct struct{}

var ctxCancelKey = ctxCancelKeyStruct{}

func cancelContext(ctx context.Context) context.Context {
	ctx, cancel := context.WithCancel(ctx)
	ctx = context.WithValue(ctx, ctxCancelKey, cancel)
	return ctx
}