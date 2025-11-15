package net

import (
	"fmt"
	"sync"

	"github.com/1143910315/UDPRelayServer/net/packer"
	"github.com/DarthPestilane/easytcp"
	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/proto"
)

var log *logrus.Logger

func init() {
	log = logrus.New()
	log.SetLevel(logrus.DebugLevel)
}

// TCPServer TCP服务器组件
type TCPServer struct {
	srv       *easytcp.Server
	sessions  map[string]easytcp.Session // 保存活跃会话
	mu        sync.RWMutex
	isRunning bool
	wg        sync.WaitGroup
}

// NewTCPServer 创建新的TCP服务器实例
func NewTCPServer() *TCPServer {
	return &TCPServer{
		sessions: make(map[string]easytcp.Session),
	}
}

// Start 启动TCP服务器
func (ts *TCPServer) Start(address string) error {
	if ts.isRunning {
		return nil
	}

	ts.srv = easytcp.NewServer(&easytcp.ServerOption{
		Packer: &packer.LengthWithIdPacker{},
		Codec:  &easytcp.ProtobufCodec{},
	})

	// 注册路由和中间件
	//ts.srv.AddRoute(packer.ID_FooReqID, ts.handleFooRequest, ts.logTransmission(&packer.FooReq{}, &packer.FooResp{}))

	// 注册会话事件处理
	ts.srv.OnSessionCreate = ts.onSessionCreate
	ts.srv.OnSessionClose = ts.onSessionClose

	ts.isRunning = true

	// 在goroutine中运行服务器
	ts.wg.Add(1)
	go func() {
		defer ts.wg.Done()
		if err := ts.srv.Run(address); err != nil {
			log.Errorf("serve err: %s", err)
		}
	}()

	log.Info("TCP server started")
	return nil
}

// Stop 停止TCP服务器
func (ts *TCPServer) Stop() {
	if !ts.isRunning {
		return
	}

	ts.isRunning = false
	if ts.srv != nil {
		ts.srv.Stop()
	}
	ts.wg.Wait()

	// 清空会话
	ts.mu.Lock()
	ts.sessions = make(map[string]easytcp.Session)
	ts.mu.Unlock()

	log.Info("TCP server stopped")
}

// IsRunning 检查服务器是否在运行
func (ts *TCPServer) IsRunning() bool {
	return ts.isRunning
}

// SendToSession 向指定会话发送数据
func (ts *TCPServer) SendToSession(sessionID string, msgID packer.ID, data []byte) error {
	ts.mu.RLock()
	session, exists := ts.sessions[sessionID]
	ts.mu.RUnlock()

	if !exists {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	lengthWithIdPacker := packer.LengthWithIdPacker{}
	packedMsg, err := lengthWithIdPacker.Pack(easytcp.NewMessage(msgID, data))
	if err != nil {
		return err
	}
	if _, err := session.Conn().Write(packedMsg); err != nil {
		return err
	}
	return nil
}

// Broadcast 广播消息到所有会话
func (ts *TCPServer) Broadcast(msgID packer.ID, data []byte) []error {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	lengthWithIdPacker := packer.LengthWithIdPacker{}
	packedMsg, err := lengthWithIdPacker.Pack(easytcp.NewMessage(msgID, data))
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

// GetSessionCount 获取当前活跃会话数量
func (ts *TCPServer) GetSessionCount() int {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return len(ts.sessions)
}

// GetSessionIDs 获取所有会话ID
func (ts *TCPServer) GetSessionIDs() []string {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	ids := make([]string, 0, len(ts.sessions))
	for id := range ts.sessions {
		ids = append(ids, id)
	}
	return ids
}

//// 处理Foo请求
//func (ts *TCPServer) handleFooRequest(c easytcp.Context) {
//	var reqData common.FooReq
//	if err := c.Bind(&reqData); err != nil {
//		log.Errorf("bind request failed: %s", err)
//		return
//	}
//
//	// 业务处理逻辑
//	log.Debugf("processing foo request: %v", reqData)
//
//	// 设置响应
//	err := c.SetResponse(common.ID_FooRespID, &common.FooResp{
//		Code:    2,
//		Message: "success",
//	})
//	if err != nil {
//		log.Errorf("set response failed: %s", err)
//	}
//}

// 日志传输中间件
func (ts *TCPServer) logTransmission(req, resp proto.Message) easytcp.MiddlewareFunc {
	return func(next easytcp.HandlerFunc) easytcp.HandlerFunc {
		return func(c easytcp.Context) {
			if err := c.Bind(req); err == nil {
				log.Debugf("recv | id: %d; size: %d; data: %s", c.Request().ID(), len(c.Request().Data()), req)
			}
			defer func() {
				respMsg := c.Response()
				if respMsg != nil {
					_ = c.Session().Codec().Decode(respMsg.Data(), resp)
					log.Infof("send | id: %d; size: %d; data: %s", respMsg.ID(), len(respMsg.Data()), resp)
				}
			}()
			next(c)
		}
	}
}

// 会话创建回调
func (ts *TCPServer) onSessionCreate(session easytcp.Session) {
	sessionID := session.ID()
	ts.mu.Lock()
	//ts.sessions[sessionID] = session
	ts.mu.Unlock()
	log.Infof("session created: %s", sessionID)
}

// 会话关闭回调
func (ts *TCPServer) onSessionClose(session easytcp.Session) {
	sessionID := session.ID()
	ts.mu.Lock()
	//delete(ts.sessions, sessionID)
	ts.mu.Unlock()
	log.Infof("session closed: %s", sessionID)
}

// 添加自定义路由
func (ts *TCPServer) AddRoute(msgID int, handler easytcp.HandlerFunc, middlewares ...easytcp.MiddlewareFunc) {
	if ts.srv != nil {
		ts.srv.AddRoute(msgID, handler, middlewares...)
	}
}
