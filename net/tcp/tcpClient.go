package tcp

import (
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/1143910315/UDPRelayServer/net/packer"
	"github.com/DarthPestilane/easytcp"
)

// TCP客户端组件
type TCPClient struct {
	conn              net.Conn
	packer            *packer.LengthWithIdPacker
	Codec             *easytcp.ProtobufCodec
	isConnected       bool
	mu                sync.RWMutex
	wg                sync.WaitGroup
	stopChan          chan struct{}
	messageChan       chan []byte
	handlers          map[packer.ID]MessageHandler
	handlerMu         sync.RWMutex
	OnLog             func(level, message string)
	addr              string
	reconnectInterval time.Duration
	reconnectChan     chan struct{}
	OnConnected       func()
	OnDisconnected    func()
}

// 消息处理器类型
type MessageHandler func(*easytcp.Message)

// 创建新的TCP客户端实例
func NewTCPClient() *TCPClient {
	return &TCPClient{
		packer:            &packer.LengthWithIdPacker{},
		Codec:             &easytcp.ProtobufCodec{},
		stopChan:          make(chan struct{}),
		messageChan:       make(chan []byte, 100),
		handlers:          make(map[packer.ID]MessageHandler),
		reconnectInterval: time.Second,
		reconnectChan:     make(chan struct{}, 1),
	}
}

// 连接到TCP服务器
func (tc *TCPClient) Connect(addr string) error {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	if tc.isConnected {
		return fmt.Errorf("client is already connected")
	}

	// 保存地址用于重连
	tc.addr = addr

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return err
	}
	return tc.setupConnection(conn, addr)
}

// 设置连接
func (tc *TCPClient) setupConnection(conn net.Conn, addr string) error {
	tc.conn = conn
	tc.isConnected = true

	// 触发连接成功事件
	if tc.OnConnected != nil {
		tc.OnConnected()
	}

	// 启动读写goroutine
	tc.wg.Add(2)
	go tc.readLoop()
	go tc.writeLoop()

	tc.log("INFO", fmt.Sprintf("TCP client connected to %s", addr))
	return nil
}

// 断开连接内部方法
func (tc *TCPClient) disconnectInternal() {
	if tc.conn != nil {
		tc.conn.Close()
		tc.conn = nil
	}

	tc.isConnected = false

	// 触发断开连接事件
	if tc.OnDisconnected != nil {
		tc.OnDisconnected()
	}
}

// 断开连接
func (tc *TCPClient) Disconnect() {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	// 设置停止标志并关闭通道，确保所有goroutine都能收到停止信号
	if !tc.isConnected && tc.stopChan != nil {
		// 如果在重连状态下，需要停止重连循环
		close(tc.stopChan)
		tc.stopChan = make(chan struct{}) // 重新创建通道以备下次使用
		tc.wg.Wait()
		return
	}

	if !tc.isConnected {
		return
	}

	tc.disconnectInternal()

	// 发送停止信号以终止所有循环（包括重连循环）
	if tc.stopChan != nil {
		close(tc.stopChan)
		tc.stopChan = make(chan struct{}) // 重新创建通道以备下次使用
	}

	tc.wg.Wait()
	tc.log("INFO", "TCP client disconnected")
}

// 重连循环
func (tc *TCPClient) reconnectLoop() {
	// 在进入重连循环时增加等待组计数
	tc.wg.Add(1)
	defer tc.wg.Done() // 确保退出时减少计数

	ticker := time.NewTicker(tc.reconnectInterval)
	defer ticker.Stop()

	for {
		select {
		case <-tc.stopChan:
			tc.log("INFO", "Reconnect loop stopped by Disconnect")
			return
		case <-ticker.C:
			tc.mu.Lock()
			if tc.isConnected {
				tc.mu.Unlock()
				continue
			}

			// 检查是否已经停止
			select {
			case <-tc.stopChan:
				tc.mu.Unlock()
				tc.log("INFO", "Reconnect loop stopped before dialing")
				return
			default:
			}

			// 尝试重连
			conn, err := net.Dial("tcp", tc.addr)
			if err != nil {
				tc.mu.Unlock()
				// tc.log("WARN", fmt.Sprintf("Reconnect failed: %v", err))
				continue
			}

			// 重连成功
			tc.setupConnection(conn, tc.addr)
			tc.mu.Unlock()
			tc.log("INFO", "Reconnected to server")
			return // 重连成功，退出重连循环
		}
	}
}

// 检查是否已连接
func (tc *TCPClient) IsConnected() bool {
	tc.mu.RLock()
	defer tc.mu.RUnlock()
	return tc.isConnected
}

// 发送消息到服务器
func (tc *TCPClient) Send(msgID packer.ID, v any) (int, error) {
	tc.mu.RLock()
	defer tc.mu.RUnlock()

	if !tc.isConnected {
		return 0, fmt.Errorf("client is not connected")
	}

	data, err := tc.Codec.Encode(v)
	if err != nil {
		return 0, err
	}
	packedMsg, err := tc.packer.Pack(easytcp.NewMessage(msgID, data))
	if err != nil {
		return 0, err
	}

	// 发送到写循环
	select {
	case tc.messageChan <- packedMsg:
		return len(packedMsg), nil
	case <-time.After(5 * time.Second):
		return 0, fmt.Errorf("send timeout")
	}
}

// 添加消息处理器
func (tc *TCPClient) AddHandler(msgID packer.ID, handler MessageHandler) {
	tc.handlerMu.Lock()
	defer tc.handlerMu.Unlock()
	tc.handlers[msgID] = handler
}

// 移除消息处理器
func (tc *TCPClient) RemoveHandler(msgID packer.ID) {
	tc.handlerMu.Lock()
	defer tc.handlerMu.Unlock()
	delete(tc.handlers, msgID)
}

// 读取循环
func (tc *TCPClient) readLoop() {
	defer tc.wg.Done()
	for {
		select {
		case <-tc.stopChan:
			return
		default:
			// 设置读取超时，用于检测连接状态
			tc.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			// 处理完整的数据包
			message, err := tc.packer.Unpack(tc.conn)
			if err != nil {
				// 检查是否是超时错误（正常情况）
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}

				// 连接断开
				tc.log("ERROR", fmt.Sprintf("Read error: %v", err))
				tc.mu.Lock()
				if tc.isConnected {
					tc.disconnectInternal()
					// 检查是否应该启动重连（确保不在停止状态）
					select {
					case <-tc.stopChan:
						tc.mu.Unlock()
						return
					default:
						// 启动重连循环
						go tc.reconnectLoop()
					}
				}
				tc.mu.Unlock()
				return
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
				tc.log("ERROR", fmt.Sprintf("Write error: %v", err))
				tc.mu.Lock()
				if tc.isConnected {
					tc.disconnectInternal()
					// 检查是否应该启动重连（确保不在停止状态）
					select {
					case <-tc.stopChan:
						tc.mu.Unlock()
						return
					default:
						// 启动重连循环
						go tc.reconnectLoop()
					}
				}
				tc.mu.Unlock()
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
		tc.log("DEBUG", fmt.Sprintf("No handler for message ID: %d, Data: %v", message.ID(), message.Data()))
	}
}

// 获取连接信息
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

// 设置读取超时时间
func (tc *TCPClient) SetReadTimeout(timeout time.Duration) error {
	tc.mu.RLock()
	defer tc.mu.RUnlock()

	if !tc.isConnected || tc.conn == nil {
		return fmt.Errorf("not connected")
	}

	return tc.conn.SetReadDeadline(time.Now().Add(timeout))
}

// 设置写入超时时间
func (tc *TCPClient) SetWriteTimeout(timeout time.Duration) error {
	tc.mu.RLock()
	defer tc.mu.RUnlock()

	if !tc.isConnected || tc.conn == nil {
		return fmt.Errorf("not connected")
	}

	return tc.conn.SetWriteDeadline(time.Now().Add(timeout))
}

// 内部日志记录方法
func (ts *TCPClient) log(level, message string) {
	if ts.OnLog != nil {
		ts.OnLog(level, message)
	}
}
