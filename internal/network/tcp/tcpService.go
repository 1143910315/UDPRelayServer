package tcp

import (
	"fmt"
	"sync"

	"github.com/1143910315/UDPRelayServer/internal/network/protocol"
	"github.com/1143910315/UDPRelayServer/internal/proto"
	"github.com/DarthPestilane/easytcp"
)

// TCP服务器组件
type TCPServer struct {
	service              *easytcp.Server
	sessions             map[string]easytcp.Session // 保存活跃会话
	mu                   sync.RWMutex
	isRunning            bool
	wg                   sync.WaitGroup
	OnClientConnected    func(sessionID string)      // 客户端连接事件回调
	OnClientDisconnected func(sessionID string)      // 客户端断开事件回调
	OnLog                func(level, message string) // 日志事件回调
}

// 创建新的TCP服务器实例
func NewTCPServer() *TCPServer {
	service := easytcp.NewServer(&easytcp.ServerOption{
		Packer: &protocol.LengthWithIDPacker{},
		Codec:  &easytcp.ProtobufCodec{},
	})
	ts := &TCPServer{
		service:   service,
		sessions:  make(map[string]easytcp.Session),
		isRunning: false,
	}
	// 注册会话事件处理
	service.OnSessionCreate = ts.onSessionCreate
	service.OnSessionClose = ts.onSessionClose
	return ts
}

// 启动TCP服务器
func (ts *TCPServer) Start(address string) error {
	ts.mu.Lock()
	if ts.isRunning {
		ts.mu.Unlock()
		return fmt.Errorf("服务器已经启动，请先停止服务器")
	}
	ts.isRunning = true
	ts.mu.Unlock()

	// 在goroutine中运行服务器
	ts.wg.Go(func() {
		if err := ts.service.Run(address); err != nil {
			ts.mu.Lock()
			if ts.isRunning {
				ts.log("error", fmt.Sprintf("服务器错误: %s", err))
			}
			ts.mu.Unlock()
		}
	})
	return nil
}

// 停止TCP服务器
func (ts *TCPServer) Stop() {
	ts.mu.Lock()
	if !ts.isRunning {
		ts.mu.Unlock()
		return
	}

	ts.isRunning = false
	ts.mu.Unlock()
	if ts.service != nil {
		ts.service.Stop()
	}
	ts.wg.Wait()

	// 清空会话
	ts.mu.Lock()
	ts.sessions = make(map[string]easytcp.Session)
	ts.mu.Unlock()
}

// 检查服务器是否在运行
func (ts *TCPServer) IsRunning() bool {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.isRunning
}

// 打包数据
func (ts *TCPServer) PackerData(msgID proto.ID, v any) ([]byte, error) {
	data, err := ts.service.Codec.Encode(v)
	if err != nil {
		return nil, err
	}
	packedMsg, err := ts.service.Packer.Pack(easytcp.NewMessage(msgID, data))
	if err != nil {
		return nil, err
	}
	return packedMsg, nil
}

// 向指定会话发送原始数据
func (ts *TCPServer) SendRawToSession(sessionID string, data []byte) error {
	ts.mu.RLock()
	session, exists := ts.sessions[sessionID]
	ts.mu.RUnlock()

	if !exists {
		return fmt.Errorf("session not found: %s", sessionID)
	}

	if _, err := session.Conn().Write(data); err != nil {
		return err
	}
	return nil
}

// 向指定会话发送数据
func (ts *TCPServer) SendToSession(sessionID string, msgID proto.ID, v any) (int, error) {
	packedMsg, err := ts.PackerData(msgID, v)
	if err != nil {
		return 0, err
	}
	if err := ts.SendRawToSession(sessionID, packedMsg); err != nil {
		return 0, err
	}
	return len(packedMsg), nil
}

// 广播消息到所有会话
func (ts *TCPServer) Broadcast(msgID proto.ID, data []byte) []error {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	packedMsg, err := ts.service.Packer.Pack(easytcp.NewMessage(msgID, data))
	if err != nil {
		return []error{err}
	}
	var errors []error
	for _, session := range ts.sessions {
		if _, err := session.Conn().Write(packedMsg); err != nil {
			errors = append(errors, err)
		}
	}
	return errors
}

// 获取当前活跃会话数量
func (ts *TCPServer) GetSessionCount() int {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return len(ts.sessions)
}

// 获取所有会话ID
func (ts *TCPServer) GetSessionIDs() []string {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	ids := make([]string, 0, len(ts.sessions))
	for id := range ts.sessions {
		ids = append(ids, id)
	}
	return ids
}

// 会话创建回调
func (ts *TCPServer) onSessionCreate(session easytcp.Session) {
	sessionID := session.ID()
	ts.mu.Lock()
	ts.sessions[sessionID.(string)] = session
	ts.mu.Unlock()

	// 触发客户端连接事件
	if ts.OnClientConnected != nil {
		ts.OnClientConnected(sessionID.(string))
	}
}

// 会话关闭回调
func (ts *TCPServer) onSessionClose(session easytcp.Session) {
	sessionID := session.ID()
	ts.mu.Lock()
	delete(ts.sessions, sessionID.(string))
	ts.mu.Unlock()

	// 触发客户端断开事件
	if ts.OnClientDisconnected != nil {
		ts.OnClientDisconnected(sessionID.(string))
	}
}

// 添加自定义路由
func (ts *TCPServer) AddRoute(msgID proto.ID, handler easytcp.HandlerFunc, middlewares ...easytcp.MiddlewareFunc) {
	if ts.service != nil {
		ts.service.AddRoute(msgID, handler, middlewares...)
	}
}

// 内部日志记录方法
func (ts *TCPServer) log(level, message string) {
	if ts.OnLog != nil {
		ts.OnLog(level, message)
	}
}
