package net

import (
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/1143910315/UDPRelayServer/net/packer"
	"github.com/DarthPestilane/easytcp"
	"github.com/sirupsen/logrus"
)

// TCPClient TCP客户端组件
type TCPClient struct {
	conn        net.Conn
	packer      *packer.LengthWithIdPacker
	codec       *easytcp.ProtobufCodec
	isConnected bool
	mu          sync.RWMutex
	wg          sync.WaitGroup
	stopChan    chan struct{}
	messageChan chan []byte
	handlers    map[packer.ID]MessageHandler
	handlerMu   sync.RWMutex
	log         *logrus.Logger
}

// MessageHandler 消息处理器类型
type MessageHandler func(*easytcp.Message)

// NewTCPClient 创建新的TCP客户端实例
func NewTCPClient() *TCPClient {
	return &TCPClient{
		packer:      &packer.LengthWithIdPacker{},
		codec:       &easytcp.ProtobufCodec{},
		stopChan:    make(chan struct{}),
		messageChan: make(chan []byte, 100),
		handlers:    make(map[packer.ID]MessageHandler),
		log:         logrus.New(),
	}
}

// Connect 连接到TCP服务器
func (tc *TCPClient) Connect(addr string) error {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	if tc.isConnected {
		return fmt.Errorf("client is already connected")
	}

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to connect to server: %v", err)
	}

	tc.conn = conn
	tc.isConnected = true

	// 启动读写goroutine
	tc.wg.Add(2)
	go tc.readLoop()
	go tc.writeLoop()

	tc.log.Infof("TCP client connected to %s", addr)
	return nil
}

// Disconnect 断开连接
func (tc *TCPClient) Disconnect() {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	if !tc.isConnected {
		return
	}

	tc.isConnected = false
	close(tc.stopChan)

	if tc.conn != nil {
		tc.conn.Close()
	}

	tc.wg.Wait()
	tc.log.Info("TCP client disconnected")
}

// IsConnected 检查是否已连接
func (tc *TCPClient) IsConnected() bool {
	tc.mu.RLock()
	defer tc.mu.RUnlock()
	return tc.isConnected
}

// Send 发送消息到服务器
func (tc *TCPClient) Send(data []byte) error {
	tc.mu.RLock()
	defer tc.mu.RUnlock()

	if !tc.isConnected {
		return fmt.Errorf("client is not connected")
	}

	// 发送到写循环
	select {
	case tc.messageChan <- data:
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("send timeout")
	}
}

// AddHandler 添加消息处理器
func (tc *TCPClient) AddHandler(msgID packer.ID, handler MessageHandler) {
	tc.handlerMu.Lock()
	defer tc.handlerMu.Unlock()
	tc.handlers[msgID] = handler
}

// RemoveHandler 移除消息处理器
func (tc *TCPClient) RemoveHandler(msgID packer.ID) {
	tc.handlerMu.Lock()
	defer tc.handlerMu.Unlock()
	delete(tc.handlers, msgID)
}

// SetLogger 设置日志记录器
func (tc *TCPClient) SetLogger(logger *logrus.Logger) {
	tc.log = logger
}

// 读取循环
func (tc *TCPClient) readLoop() {
	defer tc.wg.Done()
	for {
		select {
		case <-tc.stopChan:
			return
		default:
			// 处理完整的数据包
			message, err := tc.packer.Unpack(tc.conn)
			if err != nil {
				break // 无法读取数据，需要关闭连接
			}

			// 处理消息
			tc.handleMessage(message)
		}
	}
}

// 写入循环
func (tc *TCPClient) writeLoop() {
	defer tc.wg.Done()

	for {
		select {
		case <-tc.stopChan:
			return
		case message := <-tc.messageChan:
			if message == nil {
				continue
			}

			_, err := tc.conn.Write(message)
			if err != nil {
				tc.log.Errorf("Write error: %v", err)
				tc.Disconnect()
				return
			}
		}
	}
}

// 处理接收到的消息
func (tc *TCPClient) handleMessage(message *easytcp.Message) {
	tc.handlerMu.RLock()
	handler, exists := tc.handlers[packer.ID(message.ID().(packer.ID))]
	tc.handlerMu.RUnlock()

	if exists {
		handler(message)
	} else {
		tc.log.Debugf("No handler for message ID: %d, Data: %v", message.ID(), message.Data())
	}
}

// GetConnectionInfo 获取连接信息
func (tc *TCPClient) GetConnectionInfo() string {
	tc.mu.RLock()
	defer tc.mu.RUnlock()

	if !tc.isConnected || tc.conn == nil {
		return "Not connected"
	}

	localAddr := tc.conn.LocalAddr().String()
	remoteAddr := tc.conn.RemoteAddr().String()
	return fmt.Sprintf("Local: %s, Remote: %s", localAddr, remoteAddr)
}

// SetReadTimeout 设置读取超时时间
func (tc *TCPClient) SetReadTimeout(timeout time.Duration) error {
	tc.mu.RLock()
	defer tc.mu.RUnlock()

	if !tc.isConnected || tc.conn == nil {
		return fmt.Errorf("not connected")
	}

	return tc.conn.SetReadDeadline(time.Now().Add(timeout))
}

// SetWriteTimeout 设置写入超时时间
func (tc *TCPClient) SetWriteTimeout(timeout time.Duration) error {
	tc.mu.RLock()
	defer tc.mu.RUnlock()

	if !tc.isConnected || tc.conn == nil {
		return fmt.Errorf("not connected")
	}

	return tc.conn.SetWriteDeadline(time.Now().Add(timeout))
}
