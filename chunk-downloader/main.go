package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"time"
)

const (
	size = 10<<20 + 123 // 故意不整除，好看清最后一块的兜底
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
	for i := 0; i < n; i++ {
		s := int64(i) * per
		e := s + per - 1
		if i == n-1 {
			e = size - 1 // 兜底：除不尽的余数全归最后一块
		}
		cs = append(cs, Chunk{Index: i, Start: s, End: e})
	}
	return cs
}

// download 把每块直接写进目标文件的对应区间，不在内存里攒数据。
func download(url, dst string, cs []Chunk) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()

	total := cs[len(cs)-1].End + 1
	if err := f.Truncate(total); err != nil {
		return err
	}
	fmt.Printf("预分配: %s  %d 字节（没写到的地方是零值）\n", dst, total)

	var wg sync.WaitGroup
	errs := make(chan error, len(cs)) // 只传错误，数据已经落盘

	for _, c := range cs {
		wg.Add(1)
		go func(c Chunk) { // 显式传参，别直接捕获循环变量
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodGet, url, nil)
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", c.Start, c.End))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				errs <- err
				return
			}
			defer resp.Body.Close() // 漏了会耗尽连接池
			// 第一篇的两个坑，一个都不能省
			if resp.StatusCode != http.StatusPartialContent {
				errs <- fmt.Errorf("块 %d：期望 206，实际 %d", c.Index, resp.StatusCode)
				return
			}
			// 每块自带 offset：OffsetWriter 内部走 f.WriteAt，不动共享文件偏移
			w := io.NewOffsetWriter(f, c.Start)
			written, err := io.Copy(w, resp.Body)
			if err != nil {
				errs <- err
				return
			}
			if written != c.End-c.Start+1 {
				errs <- fmt.Errorf("块 %d：写入 %d 字节，期望 %d", c.Index, written, c.End-c.Start+1)
				return
			}
			fmt.Printf("  块 %d → offset=%-8d %d 字节\n", c.Index, c.Start, written)
		}(c)
	}

	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			return e
		}
	}
	return f.Sync() // 把缓冲刷到磁盘
}

func main() {
	const dst = "big.bin"
	want := sha256.Sum256(data)
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
	cs := split(total, n)
	fmt.Printf("文件 %d 字节，切成 %d 块\n", total, len(cs))

	// 并发下载，各块直接落盘
	if err := download(url, dst, cs); err != nil {
		panic(err)
	}

	// 重新打开文件算 SHA256，和靶场那份内容比对
	f, err := os.Open(dst)
	if err != nil {
		panic(err)
	}
	h := sha256.New()
	written, err := io.Copy(h, f)
	f.Close()
	if err != nil {
		panic(err)
	}

	got := hex.EncodeToString(h.Sum(nil))
	fmt.Println("--- 结果 ---")
	fmt.Println("落盘文件大小:", written)
	fmt.Println("原文件 SHA256:", hex.EncodeToString(want[:]))
	fmt.Println("落盘 SHA256 :", got)
	fmt.Println("内容一致    :", got == hex.EncodeToString(want[:]))
	fmt.Println("文件留在当前目录:", dst, "（看完可删）")
}
