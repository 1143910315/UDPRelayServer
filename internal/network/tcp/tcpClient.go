package tcp

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/1143910315/UDPRelayServer/internal/network/protocol"
	"github.com/1143910315/UDPRelayServer/internal/proto"
	"github.com/DarthPestilane/easytcp"
)

// TCP客户端组件
type TCPClient struct {
	conn              net.Conn
	packer            *protocol.LengthWithIDPacker
	isConnected       bool
	mu                sync.RWMutex
	wg                sync.WaitGroup
	messageChan       chan []byte
	handlers          map[proto.ID]MessageHandler
	handlerMu         sync.RWMutex
	addr              string
	ctx               context.Context
	cancel            context.CancelFunc
	reconnectInterval time.Duration

	Codec          *easytcp.ProtobufCodec
	OnLog          func(level, message string)
	OnConnected    func()
	OnDisconnected func()
}

// 消息处理器类型
type MessageHandler func(*easytcp.Message)

// 创建新的TCP客户端实例
func NewTCPClient() *TCPClient {
	ctx, cancel := context.WithCancel(context.Background())

	return &TCPClient{
		packer:            &protocol.LengthWithIDPacker{},
		Codec:             &easytcp.ProtobufCodec{},
		messageChan:       make(chan []byte, 100),
		handlers:          make(map[proto.ID]MessageHandler),
		ctx:               ctx,
		cancel:            cancel,
		reconnectInterval: time.Second,
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
	return tc.setupConnection(conn)
}

// 设置连接
func (tc *TCPClient) setupConnection(conn net.Conn) error {
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

	return nil
}

// 断开连接内部方法
func (tc *TCPClient) disconnectInternal() {
	tc.isConnected = false

	if tc.conn != nil {
		tc.conn.Close()
		tc.conn = nil
	}

	// 触发断开连接事件
	if tc.OnDisconnected != nil {
		tc.OnDisconnected()
	}
}

// 断开连接
func (tc *TCPClient) Disconnect() {
	tc.mu.Lock()

	if !tc.isConnected {
		tc.mu.Unlock()
		// 如果未连接，仍然取消context以停止可能正在运行的重连循环
		tc.cancel()
		tc.wg.Wait()
		// 重新创建context以备下次使用
		tc.ctx, tc.cancel = context.WithCancel(context.Background())
		return
	}

	tc.disconnectInternal()
	tc.mu.Unlock()

	tc.cancel()  // 取消所有goroutine
	tc.wg.Wait() // 等待所有goroutine退出

	// 重新创建context以备下次连接
	tc.ctx, tc.cancel = context.WithCancel(context.Background())
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
		case <-tc.ctx.Done():
			tc.log("info", "Reconnect loop stopped by Disconnect")
			return
		case <-ticker.C:
			tc.mu.Lock()
			if tc.isConnected {
				tc.mu.Unlock()
				continue
			}

			// 检查context是否已取消
			select {
			case <-tc.ctx.Done():
				tc.mu.Unlock()
				tc.log("info", "Reconnect loop stopped before dialing")
				return
			default:
			}

			// 尝试重连
			conn, err := net.Dial("tcp", tc.addr)
			if err != nil {
				tc.mu.Unlock()
				// tc.log("warn", fmt.Sprintf("Reconnect failed: %v", err))
				continue
			}

			// 重连成功
			tc.setupConnection(conn)
			tc.mu.Unlock()
			tc.log("info", "Reconnected to server")
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
func (tc *TCPClient) Send(msgID proto.ID, v any) (int, error) {
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
	case <-tc.ctx.Done():
		return 0, fmt.Errorf("client is disconnecting")
	case <-time.After(5 * time.Second):
		return 0, fmt.Errorf("send timeout")
	}
}

// 添加消息处理器
func (tc *TCPClient) AddHandler(msgID proto.ID, handler MessageHandler) {
	tc.handlerMu.Lock()
	defer tc.handlerMu.Unlock()
	tc.handlers[msgID] = handler
}

// 移除消息处理器
func (tc *TCPClient) RemoveHandler(msgID proto.ID) {
	tc.handlerMu.Lock()
	defer tc.handlerMu.Unlock()
	delete(tc.handlers, msgID)
}

// 读取循环
func (tc *TCPClient) readLoop() {
	defer tc.wg.Done()

	// 创建本地引用，避免竞态
	ctx := tc.ctx

	for {
		select {
		case <-ctx.Done():
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

				tc.mu.Lock()
				if tc.isConnected {
					// 连接意外断开
					tc.log("error", fmt.Sprintf("Read error: %v", err))
					tc.disconnectInternal()
					// 检查context是否已取消
					select {
					case <-ctx.Done():
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

	// 创建本地引用，避免竞态
	ctx := tc.ctx

	for {
		select {
		case <-ctx.Done():
			return
		case message := <-tc.messageChan:
			if message == nil {
				continue
			}

			_, err := tc.conn.Write(message)
			if err != nil {
				tc.log("error", fmt.Sprintf("Write error: %v", err))
				tc.mu.Lock()
				if tc.isConnected {
					tc.disconnectInternal()
					// 检查context是否已取消
					select {
					case <-ctx.Done():
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
	handler, exists := tc.handlers[proto.ID(message.ID().(proto.ID))]
	tc.handlerMu.RUnlock()

	if exists {
		handler(message)
	} else {
		tc.log("debug", fmt.Sprintf("No handler for message ID: %d, Data: %v", message.ID(), message.Data()))
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
func (tc *TCPClient) log(level, message string) {
	if tc.OnLog != nil {
		tc.OnLog(level, message)
	}
}
