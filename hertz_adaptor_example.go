package main

import (
	"log"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/adaptor"
	"github.com/cloudwego/hertz/pkg/common/hlog"
)

func hertz(mux http.Handler) {

	hlog.SetLevel(hlog.LevelFatal)
	// 2. Hertz 서버 생성
	hertzServer := server.Default(
		server.WithHostPorts(":8080"),
		server.WithSenseClientDisconnection(true),
	)
	//hertzServer.NoHijackConnPool = true

	// 3. Hertz 어댑터를 사용하여 Gin 엔진을 Hertz 핸들러로 래핑
	hertzServer.Any("/*path", adaptor.HertzHandler(mux))

	log.Println("Hertz server running http.mux application on :8080")

	// 4. Hertz 서버 시작
	hertzServer.Spin()
}
