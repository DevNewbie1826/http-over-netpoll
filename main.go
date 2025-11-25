package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/DevNewbie1826/http-over-netpoll/pkg/engine"
	"github.com/DevNewbie1826/http-over-netpoll/pkg/server"

	"github.com/lxzan/gws"

	_ "net/http/pprof" // pprof 등록
)

// sseHandler는 Server-Sent Events를 스트리밍합니다.
func sseHandler(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			log.Println("Client disconnected from SSE")
			return
		case t := <-ticker.C:
			// SSE 이벤트 데이터는 "data: 메시지\r\n\r\n" 형식으로 보냅니다.
			fmt.Fprintf(w, "data: Server time is %v\r\n\r\n", t)
			flusher.Flush()
		}
	}
}

// rootHandler는 일반 HTTP 요청을 처리합니다.
func rootHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, "Welcome! Try /sse endpoint.")
}

var upgrader = gws.NewUpgrader(&Handler{}, &gws.ServerOption{
	ParallelEnabled:   true,                                 // 병렬 메시지 처리
	Recovery:          gws.Recovery,                         // 패닉 복구
	PermessageDeflate: gws.PermessageDeflate{Enabled: true}, // 압축 활성화
})

const (
	PingInterval = 5 * time.Second
	PingWait     = 10 * time.Second
)

type Handler struct{}

func (c *Handler) OnOpen(socket *gws.Conn) {
	_ = socket.SetDeadline(time.Now().Add(PingInterval + PingWait))
}

func (c *Handler) OnClose(socket *gws.Conn, err error) {}

func (c *Handler) OnPing(socket *gws.Conn, payload []byte) {
	_ = socket.SetDeadline(time.Now().Add(PingInterval + PingWait))
	_ = socket.WritePong(nil)
}

func (c *Handler) OnPong(socket *gws.Conn, payload []byte) {}

func (c *Handler) OnMessage(socket *gws.Conn, message *gws.Message) {
	defer message.Close()
	socket.WriteMessage(message.Opcode, message.Bytes())
}

// fileHandler는 파일을 서빙하며 sendfile을 활용합니다.
func fileHandler(w http.ResponseWriter, r *http.Request) {
	// 예시로 현재 실행 중인 main.go 파일을 서빙합니다.
	// 실제로는 대용량 비디오나 이미지를 서빙할 때 유용합니다.
	http.ServeFile(w, r, "style.css")
}

func main() {
	serverType := flag.String("type", "custom", "Server type: custom, hertz, or std")
	flag.Parse()

	// 1. 라우터 설정 (표준 http.ServeMux 사용)
	mux := http.NewServeMux()
	mux.HandleFunc("/", rootHandler)
	mux.HandleFunc("/file", fileHandler) // 파일 서빙 핸들러 등록
	mux.HandleFunc("/sse", sseHandler)
	mux.HandleFunc("/ws", func(writer http.ResponseWriter, request *http.Request) {
		socket, err := upgrader.Upgrade(writer, request)
		if err != nil {
			log.Printf("WebSocket upgrade failed: %v", err)
			return
		}
		go func() {
			socket.ReadLoop()
		}()
	})

	if *serverType == "hertz" {
		hertz(mux)
		return
	}

	if *serverType == "std" {
		log.Println("Standard net/http server starting on :8080...")
		if err := http.ListenAndServe(":8080", mux); err != nil {
			log.Fatalf("Standard server failed: %v", err)
		}
		return
	}

	// 2. Engine 생성 (우리의 http.Handler를 전달)
	// 요청 처리 타임아웃을 5초로 설정 (0이면 타임아웃 없음)
	eng := engine.NewEngine(mux, engine.WithRequestTimeout(5*time.Second))

	// 3. Server 생성
	// 읽기/쓰기 타임아웃 설정
	srv := server.NewServer(eng,
		server.WithReadTimeout(10*time.Second),
		server.WithWriteTimeout(10*time.Second),
	)

	log.Println("Server starting on :8080...")
	log.Println("Endpoints available: / (HTTP), /file (sendfile), /sse (Server-Sent Events), /ws (WebSocket)")

	// 4. 서버 시작
	if err := srv.Serve(":8080"); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
