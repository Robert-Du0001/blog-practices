package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"time"
)

const (
	size = 10<<20 + 123 // 定义一个 10 MiB + 123 B 的测试文件
	n    = 8
)

// 内容随位置变化：第一篇那种填满同一个字节的数据，拼错顺序看不出来。
func makeData(total int) []byte {
	b := make([]byte, total)
	for i := range b {
		b[i] = byte((i*31 + i/7) % 251)
	}
	return b
}

// 测试文件放包级一份，服务端和 main 共用，main 拿它算 SHA256。
var data = makeData(size)

// newServer 和第一篇一样：handler 只有一行 ServeContent，Range 一概不碰。
func newServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			// 坑：每个请求都必须新建一个 Reader。
			// bytes.Reader 的读偏移是有状态的，复用同一个实例，
			// 这一篇的 8 个并发 Range 请求会读到错乱的位置。
			http.ServeContent(w, r, "big.bin", time.Time{}, bytes.NewReader(data))
		}))
}

// Chunk 表示一段闭区间 [Start, End]，两端都包含。
type Chunk struct {
	Index      int
	Start, End int64
}

// split 把 size 字节的文件切成 n 段：互不重叠、首尾相接、覆盖 [0, size-1]。
func split(size int64, n int) []Chunk {
	per := size / int64(n)
	cs := make([]Chunk, 0, n)
	for i := range n {
		s := int64(i) * per
		e := s + per - 1
		if i == n-1 {
			e = size - 1 // 兜底：除不尽的余数全归最后一块
		}
		cs = append(cs, Chunk{Index: i, Start: s, End: e})
	}
	return cs
}

type Result struct {
	Index int
	Data  []byte
	Err   error
}

// download 给每块起一个 goroutine，结果按 Index 归位。
func download(url string, cs []Chunk) ([][]byte, error) {
	chunks := make([][]byte, len(cs))
	ch := make(chan Result, len(cs)) // 缓冲开成块数：中途 return 也不会有协程卡在发送上
	var wg sync.WaitGroup
	// atomic.Int32 是标准库的原子计数器，多个人同时加也不会加错，这里只用来给「第几个完成」编号
	var seq atomic.Int32

	for _, c := range cs {
		wg.Add(1)
		go func(c Chunk) { // 显式传参，别直接捕获循环变量
			defer wg.Done()

			// 仅为了演示乱序：块号小的晚一点发请求。
			// 真实下载不需要，删掉这行程序照常工作。
			time.Sleep(time.Duration(len(cs)-1-c.Index) * 3 * time.Millisecond)

			req, _ := http.NewRequest(http.MethodGet, url, nil)
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", c.Start, c.End))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				ch <- Result{Index: c.Index, Err: err}
				return
			}
			defer resp.Body.Close() // 漏了会耗尽连接池

			// 第一篇的两个坑，一个都不能省
			if resp.StatusCode != http.StatusPartialContent {
				ch <- Result{Index: c.Index,
					Err: fmt.Errorf("块 %d：期望 206，实际 %d", c.Index, resp.StatusCode)}
				return
			}
			b, err := io.ReadAll(resp.Body)
			if err == nil && int64(len(b)) != c.End-c.Start+1 {
				err = fmt.Errorf("块 %d：长度 %d，期望 %d", c.Index, len(b), c.End-c.Start+1)
			}
			if err == nil {
				fmt.Printf("  第 %d 个完成：块 %d（%d 字节）\n", seq.Add(1), c.Index, len(b))
			}
			ch <- Result{Index: c.Index, Data: b, Err: err}
		}(c)
	}

	go func() { wg.Wait(); close(ch) }() // close 必须在所有 goroutine 都发完之后

	for r := range ch {
		if r.Err != nil {
			return nil, r.Err
		}
		chunks[r.Index] = r.Data // 下标即位置，与到达顺序无关
	}
	return chunks, nil
}

func main() {
	want := sha256.Sum256(data) // data 就是靶场共用的那份测试文件
	srv := newServer()
	defer srv.Close()
	url := srv.URL + "/big.bin"

	// HEAD：拿总大小 + 确认支持分段
	head, err := http.Head(url)
	if err != nil {
		panic(err)
	}
	head.Body.Close()
	if head.Header.Get("Accept-Ranges") != "bytes" {
		panic("服务端不支持 Range，应降级为单连接下载")
	}
	total := head.ContentLength

	// 切块
	cs := split(total, n)
	fmt.Printf("文件 %d 字节，切成 %d 块（每块 %d 字节）\n", total, len(cs), cs[0].End-cs[0].Start+1)
	for _, c := range cs {
		fmt.Printf("  块 %d  Range: bytes=%d-%d  %d 字节\n",
			c.Index, c.Start, c.End, c.End-c.Start+1)
	}

	// 并发下载 + 按 Index 归位
	fmt.Println("并发下载中：")
	chunks, err := download(url, cs)
	if err != nil {
		panic(err)
	}

	// 按序拼接 + 完整性校验
	var merged bytes.Buffer
	for _, p := range chunks {
		merged.Write(p)
	}
	got := sha256.Sum256(merged.Bytes())
	fmt.Println("--- 结果 ---")
	fmt.Println("拼接字节数  :", merged.Len())
	fmt.Println("原文件 SHA256:", hex.EncodeToString(want[:]))
	fmt.Println("拼接 SHA256:", hex.EncodeToString(got[:]))
	fmt.Println("内容一致    :", got == want)
}
