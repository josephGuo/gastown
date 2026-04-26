package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gfpcom/free-proxy-list/internal"
)

var (
	proxyFile   = flag.String("file", ``, "代理文件路径")
	protocol    = flag.String("protocol", "http", "代理协议")
	ptimeout    = flag.Int("timeout", 3, "超时时间(秒)")
	concurrency = flag.Int("concurrency", 10000, "并发数")
	testURL     = flag.String("test-url", "https://www.baidu.com", "测试URL")
	outputFile  = flag.String("output", ".", "输出文件路径")
	verbose     = flag.Bool("verbose", false, "详细输出")
)

// 定义一个私有类型，用于在 Context 中传递动态代理 URL
type proxyCtxKey struct{}

func createSharedClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			// 动态代理注入逻辑
			Proxy: func(req *http.Request) (*url.URL, error) {
				if p, ok := req.Context().Value(proxyCtxKey{}).(*url.URL); ok {
					return p, nil
				}
				return nil, nil
			},
			DialContext: (&net.Dialer{
				Timeout:   timeout,
				KeepAlive: -1,
				// ==========================================================
				// 【必加 1】：禁用 IPv4/IPv6 并行拨号，解决 Windows IOCP 死锁
				// ==========================================================
				FallbackDelay: -1,
			}).DialContext,
			TLSHandshakeTimeout: timeout,
			DisableKeepAlives:   true,
			MaxIdleConns:        0,
			// ==========================================================
			// 【必加 2】：取消单主机并发连接限制，解决 getConn 死锁
			// ==========================================================
			MaxConnsPerHost: 0,
		},
	}
}

// ProxyTestResult 代理测试结果
type ProxyTestResult struct {
	Proxy    *url.URL
	Success  bool
	Latency  time.Duration
	Error    string
	TestTime time.Time
}

// ProxyTester 代理测试器
type ProxyTester struct {
	testURL      string
	results      chan ProxyTestResult
	verbose      bool
	successCount int32
	failCount    int32
}

// NewProxyTester 创建新的代理测试器
func NewProxyTester(testURL string, verbose bool, concurrency int) *ProxyTester {
	return &ProxyTester{
		testURL: testURL,

		verbose: verbose,
		results: make(chan ProxyTestResult, concurrency*3),
	}
}

// TestProxy 测试单个代理
func (pt *ProxyTester) TestProxy(ctx context.Context, proxyURL *url.URL, client *http.Client) ProxyTestResult {
	start := time.Now()
	result := ProxyTestResult{
		Proxy:    proxyURL,
		TestTime: start,
	}
	// 【关键步骤】：把当前这次要测试的代理 IP，塞进请求的 Context 里
	// 这样上面 Transport 的 Proxy 函数就能拿到它了
	reqCtx := context.WithValue(ctx, proxyCtxKey{}, proxyURL)
	req, err := http.NewRequestWithContext(reqCtx, "GET", pt.testURL, nil)
	if err != nil {
		result.Error = fmt.Sprintf("创建请求失败: %v", err)
		result.Latency = time.Since(start)
		atomic.AddInt32(&pt.failCount, 1)
		return result
	}
	// 直接使用传进来的共享 client
	resp, err := client.Do(req)
	if err != nil {
		result.Error = fmt.Sprintf("请求失败: %v", err)
		result.Latency = time.Since(start)
		atomic.AddInt32(&pt.failCount, 1)
		return result
	}
	// 读取响应内容
	//_, err = io.ReadAll(resp.Body)
	_, err = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if err != nil {
		result.Error = fmt.Sprintf("读取响应失败: %v", err)
		result.Latency = time.Since(start)
		atomic.AddInt32(&pt.failCount, 1)
		return result
	}
	result.Latency = time.Since(start)
	if resp.StatusCode == http.StatusOK {
		result.Success = true
		atomic.AddInt32(&pt.successCount, 1)
	} else {
		result.Error = fmt.Sprintf("状态码: %d", resp.StatusCode)
		atomic.AddInt32(&pt.failCount, 1)
	}
	return result
}

// LoadProxies 从文件加载代理列表
func LoadProxies(filePath string) ([]*internal.Proxy, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var proxies []*internal.Proxy
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// 解析代理URL
		proxy, err := internal.ParseProxyURL("", line)
		if err != nil || proxy == nil {
			if *verbose {
				log.Printf("⚠️  跳过无效代理: %s - %v", line, err)
			}
			continue
		}

		proxies = append(proxies, proxy)
	}
	return proxies, scanner.Err()
}

func LoadProxyURLs(filePath string, loadedCount *int32) (<-chan *url.URL, error) {
	file, err := os.Open(filePath)
	if err != nil {
		log.Printf("打开文件失败: %v", err)
		return nil, err
	}
	proxyURLChan := make(chan *url.URL, 50000)
	go func() {
		defer close(proxyURLChan)
		defer file.Close()
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			if !strings.Contains(line, "://") {
				line = "http://" + line // 补充默认协议
			}
			proxyURL, err := url.Parse(line)
			if err != nil {
				log.Printf("⚠️  跳过无效代理: %s - %v", line, err)
				continue
			}
			proxyURLChan <- proxyURL
			atomic.AddInt32(loadedCount, 1) // 边读边累加
		}

	}()
	return proxyURLChan, nil
}

func saveProtocolFile(filename, protocol string, protocolProxies []ProxyTestResult) error {
	file, err := os.Create(filename)
	if err != nil {
		log.Printf("创建文件失败: %s - %v", filename, err)
		return err
	}
	defer file.Close()
	// 写入头部信息
	file.WriteString(fmt.Sprintf("# %s 协议可用代理列表\n", strings.ToUpper(protocol)))
	file.WriteString("# 格式: 代理URL 延迟(ms) 测试时间\n")
	file.WriteString("# 生成时间: " + time.Now().Format("2006-01-02 15:04:05") + "\n\n")

	// 按延迟排序
	sort.Slice(protocolProxies, func(i, j int) bool {
		return protocolProxies[i].Latency < protocolProxies[j].Latency
	})

	// 写入代理
	for _, result := range protocolProxies {
		file.WriteString(fmt.Sprintf("%s %d %s\n",
			result.Proxy.String(),
			result.Latency.Milliseconds(),
			result.TestTime.Format("15:04:05")))
	}
	return nil
}

// SaveResults 保存测试结果
func SaveResults(results []ProxyTestResult, outputPath string) error {
	// 按协议分组保存
	protocolResults := make(map[string][]ProxyTestResult)
	for _, result := range results {
		if result.Success {
			protocol := strings.ToLower(result.Proxy.Scheme)
			protocolResults[protocol] = append(protocolResults[protocol], result)
		}
	}
	// 如果没有指定输出文件，创建默认输出目录
	if outputPath == "" {
		outputPath = "working-proxies"
	}
	// 创建输出目录
	if err := os.MkdirAll(outputPath, 0755); err != nil {
		return err
	}
	// 为每个协议创建单独的文件
	for protocol, protocolProxies := range protocolResults {
		filename := filepath.Join(outputPath, protocol+".txt")
		if err := saveProtocolFile(filename, protocol, protocolProxies); err != nil {
			log.Printf("保存 %s 代理到 %s 失败: %v", protocol, filename, err)
			continue
		}
		log.Printf("保存 %d 个 %s 代理到: %s", len(protocolProxies), strings.ToUpper(protocol), filename)
	}
	// 创建汇总文件
	summaryFile := filepath.Join(outputPath, "summary.txt")
	file, err := os.Create(summaryFile)
	if err != nil {
		return err
	}
	defer file.Close()
	file.WriteString("# 代理测试汇总\n")
	file.WriteString("# 生成时间: " + time.Now().Format("2006-01-02 15:04:05") + "\n\n")
	totalSuccess := 0
	for protocol, protocolProxies := range protocolResults {
		totalSuccess += len(protocolProxies)
		file.WriteString(fmt.Sprintf("%s: %d 个可用代理\n", strings.ToUpper(protocol), len(protocolProxies)))
	}

	if len(results) > 0 {
		file.WriteString(fmt.Sprintf("\n总计: %d / %d (%.2f%%)\n",
			totalSuccess, len(results),
			float64(totalSuccess)/float64(len(results))*100))
	} else {
		file.WriteString("\n总计: 0 / 0 (0.00%)\n")
	}

	log.Printf("测试汇总已保存到: %s", summaryFile)
	return nil
}

func main() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("捕获 panic: %v\n", r)
			fmt.Println("\n堆栈跟踪:")
			debug.PrintStack()
		}
	}()
	flag.Parse()

	if *proxyFile == "" {
		// 使用默认路径E:\GOWP\src\josephGuo\free-proxy-list\list\http.txt
		cwd, _ := os.Getwd()
		*proxyFile = filepath.Join(cwd, "list", *protocol+".txt")
	}

	log.Printf("开始测试代理文件: %s", *proxyFile)
	log.Printf("测试URL: %s", *testURL)
	log.Printf("超时时间: %d秒", *ptimeout)
	log.Printf("并发数: %d", *concurrency)
	ctx := context.Background()
	// 开始测试
	startTime := time.Now()
	log.Printf("开始测试...")

	// 加载代理列表
	var loadedCount int32 // 原子计数器，记录后台读取了多少个
	proxyURLChs, err := LoadProxyURLs(*proxyFile, &loadedCount)
	if err != nil {
		log.Printf("加载代理URL失败: %v", err)
		return
	}
	var completed int32
	// 【关键】用于记录历史最大分母，防止进度条倒退
	var maxTotalSoFar int32

	// 1. 创建全局唯一的共享 Client
	timeout := time.Duration(*ptimeout) * time.Second
	sharedClient := createSharedClient(timeout)
	defer sharedClient.Transport.(*http.Transport).CloseIdleConnections()

	// 创建测试器
	tester := NewProxyTester(*testURL, *verbose, *concurrency)

	// 创建工作池

	var wg sync.WaitGroup

	// 结果收集
	var results = make([]ProxyTestResult, 0, 100000)

	// 启动结果收集goroutine
	resCh := make(chan struct{}, 1)
	go func() {
		for result := range tester.results {
			results = append(results, result)
		}
		resCh <- struct{}{}
	}()

	// 3. 启动 Worker Pool

	actualConcurrency := *concurrency

	for w := 0; w < actualConcurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for proxyURL := range proxyURLChs {
				// 测试代理
				result := tester.TestProxy(ctx, proxyURL, sharedClient)
				tester.results <- result
				cnt := atomic.AddInt32(&completed, 1)
				// 显示进度
				if cnt%5000 == 0 {
					currentTotal := atomic.LoadInt32(&loadedCount)
					// 【核心技巧】使用 CAS 保证 maxTotalSoFar 只增不减
					for {
						oldMax := atomic.LoadInt32(&maxTotalSoFar)
						if currentTotal <= oldMax {
							currentTotal = oldMax // 使用稳定下来的最大值作为分母
							break
						}
						if atomic.CompareAndSwapInt32(&maxTotalSoFar, oldMax, currentTotal) {
							break // 成功更新最大值，跳出
						}
					}

					if currentTotal > 0 {
						pct := float64(cnt) / float64(currentTotal) * 100
						log.Printf("进度: %d / ~%d (%.1f%%)", cnt, currentTotal, pct)
					}
				}
			}

		}()
	}

	// 等待所有测试完成
	wg.Wait()
	close(tester.results)
	// 等待结果收集完成
	<-resCh
	close(resCh)

	finalTotal := atomic.LoadInt32(&loadedCount)
	finalCompleted := atomic.LoadInt32(&completed)
	log.Printf("进度: %d / %d (100.0%%)", finalCompleted, finalTotal)
	// 排序结果（按延迟）
	sort.Slice(results, func(i, j int) bool {
		return results[i].Latency < results[j].Latency
	})

	// 输出结果
	duration := time.Since(startTime)
	successCount := 0
	totalLatency := time.Duration(0)

	fmt.Println("\n" + strings.Repeat("=", 80))
	fmt.Printf("测试完成! 总用时: %v\n", duration)
	fmt.Printf("测试代理数: %d\n", finalTotal)
	fmt.Printf("成功代理数: %d\n", atomic.LoadInt32(&tester.successCount))
	fmt.Printf("失败代理数: %d\n", atomic.LoadInt32(&tester.failCount))
	fmt.Printf("成功率: %.2f%%\n", float64(atomic.LoadInt32(&tester.successCount))/float64(finalTotal)*100)

	// 显示最快的10个代理
	fmt.Println("\n最快的10个代理:")
	fmt.Println(strings.Repeat("-", 80))
	fmt.Printf("%-5s %-30s %-10s %-10s\n", "排名", "代理地址", "延迟(ms)", "状态")
	fmt.Println(strings.Repeat("-", 80))

	rank := 1
	for _, result := range results {
		if result.Success {
			fmt.Printf("%-5d %-30s %-10d %-10s\n",
				rank,
				result.Proxy.String(),
				result.Latency.Milliseconds(),
				"✅")
			successCount++
			totalLatency += result.Latency
			rank++
			if rank > 10 {
				break
			}
		}
	}

	if successCount > 0 {
		fmt.Printf("\n平均延迟: %v\n", totalLatency/time.Duration(successCount))
	}
	// 保存结果到文件
	if *outputFile != "" {
		err := SaveResults(results, *outputFile)
		if err != nil {
			log.Printf("保存结果失败: %v", err)
		} else {
			log.Printf("结果已保存到: %s", *outputFile)
		}
	}

	// 显示失败的代理（如果有）
	if *verbose && atomic.LoadInt32(&tester.failCount) > 0 {
		fmt.Println("\n失败的代理:")
		fmt.Println(strings.Repeat("-", 80))
		fmt.Printf("%-30s %-20s\n", "代理地址", "错误信息")
		fmt.Println(strings.Repeat("-", 80))

		failCount := 0
		for _, result := range results {
			if !result.Success {
				fmt.Printf("%-30s %-20s\n", result.Proxy.String(), result.Error)
				failCount++
				if failCount >= 20 { // 只显示前20个失败的
					fmt.Printf("... 还有 %d 个失败的代理\n", atomic.LoadInt32(&tester.failCount)-20)
					break
				}
			}
		}
	}
}
