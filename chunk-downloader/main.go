package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"time"
)

// newServer 造一个 sizeMB 大小的假文件服务，内容可预测，零外网依赖。
func newServer(sizeMB int) *httptest.Server {
	data := bytes.Repeat([]byte("0123456789ABCDEF"), sizeMB*1024*1024/16)
	return httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			// 坑：每个请求都必须新建一个 Reader。
			// bytes.Reader 的读偏移是有状态的，复用同一个实例，
			// 并发的 Range 请求会读到错乱的位置。
			http.ServeContent(w, r, "big.bin", time.Time{}, bytes.NewReader(data))
		}))
}

func main() {
	srv := newServer(8) // 8 MB
	defer srv.Close()
	url := srv.URL + "/big.bin"

	// HEAD 探测：拿总大小，确认是否支持分段
	head, err := http.Head(url)
	if err != nil {
		panic(err)
	}
	head.Body.Close()
	fmt.Println("--- HEAD ---")
	fmt.Println("Status        :", head.Status)
	fmt.Println("Content-Length:", head.ContentLength)
	fmt.Println("Accept-Ranges :", head.Header.Get("Accept-Ranges"))

	// 带 Range 的 GET：只取前 1024 字节
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Range", "bytes=0-1023")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()

	fmt.Println("--- GET with Range ---")
	fmt.Println("Status        :", resp.Status)
	fmt.Println("Content-Range :", resp.Header.Get("Content-Range"))
	fmt.Println("Content-Length:", resp.ContentLength)
	body, _ := io.ReadAll(resp.Body)
	fmt.Println("实际收到字节  :", len(body))
}
