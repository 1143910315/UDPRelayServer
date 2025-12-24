package udp

import (
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// 日志回调函数类型
type LogCallback func(level string, message string)

// 接收数据回调函数类型
type DataCallback func(data []byte, addr *net.UDPAddr)

// 发送完成回调函数类型
type SendCallback func(data []byte, addr *net.UDPAddr, err error)

// UDP服务配置
type ServiceConfig struct {
	Port         int           // 监听端口
	BufferSize   int           // 接收缓冲区大小
	MaxWorkers   int           // 最大工作协程数
	ReadTimeout  time.Duration // 读取超时时间
	WriteTimeout time.Duration // 写入超时时间
}

// 默认配置
func DefaultConfig() *ServiceConfig {
	return &ServiceConfig{
		Port:         0,                // 0 表示随机端口
		BufferSize:   65507,            // UDP最大数据包大小
		MaxWorkers:   10,               // 最大工作协程数
		ReadTimeout:  30 * time.Second, // 读取超时
		WriteTimeout: 30 * time.Second, // 写入超时
	}
}

// UDP服务
type UDPService struct {
	mu        sync.RWMutex
	config    *ServiceConfig
	conn      *net.UDPConn
	isRunning atomic.Bool
	isStopped chan struct{}
	wg        sync.WaitGroup

	// 回调函数
	logCallback  LogCallback
	dataCallback DataCallback

	// 异步发送队列
	sendQueue   chan *udpMessage
	sendWorkers int
}

// UDP消息结构
type udpMessage struct {
	data     []byte
	addr     *net.UDPAddr
	callback SendCallback
}

// 创建新的UDP服务
func NewUDPService(config *ServiceConfig) *UDPService {
	if config == nil {
		config = DefaultConfig()
	}

	return &UDPService{
		config:    config,
		isStopped: make(chan struct{}),
		sendQueue: make(chan *udpMessage, 1000), // 发送队列缓冲
	}
}

// 设置日志回调函数
func (s *UDPService) SetLogCallback(callback LogCallback) {
	s.mu.Lock()
	s.logCallback = callback
	s.mu.Unlock()
}

// 设置数据接收回调函数
func (s *UDPService) SetDataCallback(callback DataCallback) {
	s.mu.Lock()
	s.dataCallback = callback
	s.mu.Unlock()
}

// 内部日志函数
func (s *UDPService) log(level string, format string, args ...any) {
	s.mu.RLock()
	callback := s.logCallback
	s.mu.RUnlock()

	if callback != nil {
		callback(level, fmt.Sprintf(format, args...))
	} else {
		// 默认日志输出
		log.Printf("[%s] %s", level, fmt.Sprintf(format, args...))
	}
}

// 启动UDP服务
func (s *UDPService) Start() error {
	if s.isRunning.Load() {
		return errors.New("service is already running")
	}

	// 解析监听地址
	addr := fmt.Sprintf(":%d", s.config.Port)
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return err
	}

	// 创建UDP连接
	var conn *net.UDPConn
	conn, err = net.ListenUDP("udp", udpAddr)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.conn = conn
	s.mu.Unlock()

	// 设置超时
	if s.config.ReadTimeout > 0 {
		if err := conn.SetReadDeadline(time.Now().Add(s.config.ReadTimeout)); err != nil {
			s.log("warn", "Failed to set read timeout: %v", err)
		}
	}

	s.isRunning.Store(true)
	close(s.isStopped)
	s.isStopped = make(chan struct{})

	// 启动发送工作协程
	s.startSendWorkers()

	// 启动接收协程
	s.wg.Add(1)
	go s.receiveLoop()

	return nil
}

// 启动发送工作协程
func (s *UDPService) startSendWorkers() {
	for i := 0; i < s.config.MaxWorkers; i++ {
		s.wg.Add(1)
		go s.sendWorker(i)
		s.sendWorkers++
	}
	s.log("info", "Started %d send workers", s.config.MaxWorkers)
}

// 发送工作协程
func (s *UDPService) sendWorker(id int) {
	defer s.wg.Done()

	for {
		select {
		case <-s.isStopped:
			s.log("debug", "Send worker %d stopped", id)
			return
		case msg := <-s.sendQueue:
			if msg == nil {
				continue
			}

			s.sendDirect(msg.data, msg.addr, msg.callback)
		}
	}
}

// 接收循环
func (s *UDPService) receiveLoop() {
	defer s.wg.Done()

	buffer := make([]byte, s.config.BufferSize)

	for s.isRunning.Load() {
		select {
		case <-s.isStopped:
			return
		default:
			// 设置读取超时，以便可以检查停止信号
			if s.config.ReadTimeout > 0 {
				s.conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			}

			n, addr, err := s.conn.ReadFromUDP(buffer)
			if err != nil {
				// 超时错误是预期的，用于检查停止信号
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}

				if s.isRunning.Load() {
					s.log("error", "Error reading from UDP: %v", err)
				}
				continue
			}

			// 复制数据，避免在回调中被修改
			data := make([]byte, n)
			copy(data, buffer[:n])

			// 调用数据回调
			s.mu.RLock()
			callback := s.dataCallback
			s.mu.RUnlock()

			if callback != nil {
				// 异步处理数据，避免阻塞接收
				go func(data []byte, addr *net.UDPAddr) {
					defer func() {
						if r := recover(); r != nil {
							s.log("error", "Panic in data callback: %v", r)
						}
					}()
					callback(data, addr)
				}(data, addr)
			}
		}
	}
}

// 发送UDP数据（同步）
func (s *UDPService) Send(data []byte, addr string) error {
	if !s.isRunning.Load() {
		return errors.New("service is not running")
	}

	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return err
	}

	s.mu.RLock()
	conn := s.conn
	s.mu.RUnlock()

	if conn == nil {
		return errors.New("connection is not available")
	}

	// 设置写超时
	if s.config.WriteTimeout > 0 {
		conn.SetWriteDeadline(time.Now().Add(s.config.WriteTimeout))
	}

	_, err = conn.WriteToUDP(data, udpAddr)
	if err != nil {
		return err
	}
	return nil
}

// 异步发送UDP数据
func (s *UDPService) SendAsync(data []byte, addr string, callback SendCallback) error {
	if !s.isRunning.Load() {
		return errors.New("service is not running")
	}

	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		s.log("error", "Failed to resolve address %s: %v", addr, err)
		if callback != nil {
			callback(data, nil, err)
		}
		return err
	}

	// 放入发送队列
	select {
	case s.sendQueue <- &udpMessage{
		data:     data,
		addr:     udpAddr,
		callback: callback,
	}:
		return nil
	default:
		err := errors.New("send queue is full")
		s.log("error", "Send queue is full")
		if callback != nil {
			callback(data, udpAddr, err)
		}
		return err
	}
}

// 直接发送（由工作协程调用）
func (s *UDPService) sendDirect(data []byte, addr *net.UDPAddr, callback SendCallback) {
	s.mu.RLock()
	conn := s.conn
	s.mu.RUnlock()

	if conn == nil {
		if callback != nil {
			callback(data, addr, errors.New("connection is not available"))
		}
		return
	}

	// 设置写超时
	if s.config.WriteTimeout > 0 {
		conn.SetWriteDeadline(time.Now().Add(s.config.WriteTimeout))
	}

	_, err := conn.WriteToUDP(data, addr)

	if callback != nil {
		callback(data, addr, err)
	}
}

// 停止UDP服务
func (s *UDPService) Stop() error {
	if !s.isRunning.Load() {
		return errors.New("service is not running")
	}

	// 设置停止标志
	s.isRunning.Store(false)

	// 通知所有协程停止
	close(s.isStopped)

	// 关闭连接
	s.mu.Lock()
	if s.conn != nil {
		s.conn.Close()
		s.conn = nil
	}
	s.mu.Unlock()

	// 清空发送队列
	go func() {
		for i := 0; i < s.sendWorkers; i++ {
			s.sendQueue <- nil
		}
	}()

	// 等待所有协程结束
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	// 超时保护
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}

	return nil
}

// 获取服务运行状态
func (s *UDPService) IsRunning() bool {
	if s.isRunning.Load() {
		return true
	}
	return false
}
