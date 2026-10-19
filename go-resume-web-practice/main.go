package main

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
)

//go:embed web
var webFiles embed.FS

func main() {
	// 从嵌入的文件系统中取出 web 目录
	pageFS, err := fs.Sub(webFiles, "web")
	if err != nil {
		log.Fatal(err)
	}

	// 把 web 目录里的文件对外提供访问
	http.Handle("/", http.FileServer(http.FS(pageFS)))

	log.Println("Web 服务器启动中，本机访问: http://localhost:8080")
	log.Println("同一局域网的其他设备访问: http://本机内网IP:8080")

	// 监听 0.0.0.0，允许局域网内的其他设备访问
	if err := http.ListenAndServe("0.0.0.0:8080", nil); err != nil {
		log.Fatal(err)
	}
}
