package engine

import (
	"context"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/DevNewbie1826/http-over-netpoll/pkg/adaptor"
	"github.com/DevNewbie1826/http-over-netpoll/pkg/appcontext"

	"github.com/cloudwego/netpoll"
)

// Option is a function type for configuring the Engine.
// Option은 Engine 설정을 위한 함수 타입입니다.
type Option func(*Engine)

// WithRequestTimeout sets the request processing timeout.
// WithRequestTimeout은 요청 처리 타임아웃을 설정합니다.
func WithRequestTimeout(d time.Duration) Option {
	return func(e *Engine) {
		e.requestTimeout = d
	}
}

// Engine is the core structure for processing HTTP requests.
// Engine은 HTTP 요청을 처리하는 핵심 구조체입니다.
type Engine struct {
	Handler        http.Handler
	requestTimeout time.Duration
}

// NewEngine creates a new Engine.
// NewEngine은 새로운 Engine을 생성합니다.
func NewEngine(handler http.Handler, opts ...Option) *Engine {
	e := &Engine{
		Handler: handler,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// ServeConn is used as netpoll's OnRequest callback.
// ServeConn은 netpoll의 OnRequest 콜백으로 사용됩니다.
func (e *Engine) ServeConn(ctx context.Context, conn netpoll.Connection) error {
	for {
		requestContext := appcontext.NewRequestContext(conn, ctx)

		req, hijacked, err := e.handleRequest(requestContext)

		if err != nil {
			requestContext.Release()
			return err
		}

		if hijacked {
			<-ctx.Done()
			return nil
		}

		// Body Draining: Read and discard remaining body data for the next request.
		// Body Draining: 다음 요청을 위해 남은 바디 데이터를 읽어서 버립니다.
		if req.Body != nil {
			_, _ = io.Copy(io.Discard, req.Body)
			_ = req.Body.Close()
		}

		requestContext.Release()

		// Keep-alive logic: Decides whether to close the connection based on the request.
		// keep-alive 로직: 요청에 따라 연결을 닫을지 결정합니다.
		if req.Close || req.Header.Get("Connection") == "close" {
			return nil
		}
	}
}

// handleRequest processes a single HTTP request and returns the processed request object and hijacking status.
// handleRequest는 단일 HTTP 요청을 처리하고, 처리된 요청 객체와 하이재킹 여부를 반환합니다.
func (e *Engine) handleRequest(ctx *appcontext.RequestContext) (*http.Request, bool, error) {
	req, err := adaptor.GetRequest(ctx)
	if err != nil {
		return nil, false, err
	}

	respWriter := adaptor.NewResponseWriter(ctx, req)
	defer respWriter.Release()

	// Apply Request Timeout.
	// 요청 타임아웃 적용.
	var cancel context.CancelFunc
	if e.requestTimeout > 0 {
		var timeoutCtx context.Context
		timeoutCtx, cancel = context.WithTimeout(req.Context(), e.requestTimeout)
		req = req.WithContext(timeoutCtx)
	}

	// Panic Recovery
	// 패닉 복구
	func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[Panic] Recovered in handler: %v", r)
				respWriter.WriteHeader(http.StatusInternalServerError)
			}
		}()
		e.Handler.ServeHTTP(respWriter, req)
	}()

	if cancel != nil {
		cancel()
	}

	err = respWriter.EndResponse()
	if err != nil {
		return nil, false, err
	}

	return req, respWriter.Hijacked(), nil
}