package session

import (
	"errors"
	"fmt"
	"net"
	"strconv"

	"github.com/1143910315/UDPRelayServer/internal/game/data"
	"github.com/1143910315/UDPRelayServer/internal/network/tcp"
	"github.com/1143910315/UDPRelayServer/internal/network/udp"
	"github.com/1143910315/UDPRelayServer/internal/proto"
	"github.com/DarthPestilane/easytcp"
)

// 主机会话
type HostSession struct {
	tcpService                   *tcp.TCPServer
	reservePort                  map[string]int
	udpRelay                     map[int]*udp.UDPService
	targetPort                   int
	PlayerManager                *data.PlayerManager
	OnNeedGenerateAuthentication func(sessionID string) (authenticationID string, deviceID string)
	OnPlayerInfoUpdated          func()
	OnLog                        func(level, message string)
	GetDeviceRemark              func(deviceID string) string
	GetKeyPairByServiceIDIndex   func(serviceIDIndex int) (serviceId string, publicKey string, err error)
	CheckDeviceAuthentication    func(serviceIDIndex int, deviceID string, authenticationID string) bool
}

// 创建主机会话
func NewHostSession() *HostSession {
	return &HostSession{
		reservePort:   make(map[string]int),
		udpRelay:      make(map[int]*udp.UDPService),
		PlayerManager: data.NewPlayerManager(),
	}
}

// 启动服务
func (s *HostSession) Start(address string, targetPort int) error {
	s.targetPort = targetPort
	s.tcpService = tcp.NewTCPServer()
	s.tcpService.OnLog = s.OnLog
	s.tcpService.OnClientConnected = func(sessionID string) {
		player := data.NewPlayer()
		player.SessionID = sessionID
		s.PlayerManager.PlayersMutex.Lock()
		s.PlayerManager.Players = append(s.PlayerManager.Players, player)
		s.PlayerManager.PlayersMutex.Unlock()

		s.OnLog("info", fmt.Sprintf("客户端连接: %s", sessionID))
		s.OnPlayerInfoUpdated()

		serviceID, publicKey, err := s.GetKeyPairByServiceIDIndex(0)
		if err != nil {
			s.OnLog("error", "获取密钥对失败: "+err.Error())
			return
		}
		req := &proto.ServiceIDPackage{
			Index:     0,
			ServiceId: serviceID,
			PublicKey: publicKey,
		}
		sendBytesSize, err := s.tcpService.SendToSession(sessionID, proto.ID_ServiceIDPackageID, req)
		if err != nil {
			s.OnLog("error", "发送密钥对失败: "+err.Error())
			return
		}
		s.PlayerManager.PlayersMutex.Lock()
		player.TotalUpload += int64(sendBytesSize)
		s.PlayerManager.PlayersMutex.Unlock()
	}
	s.tcpService.OnClientDisconnected = func(sessionID string) {
		s.PlayerManager.PlayersMutex.Lock()
		for i, player := range s.PlayerManager.Players {
			if player.SessionID == sessionID {
				s.PlayerManager.Players = append(s.PlayerManager.Players[:i], s.PlayerManager.Players[i+1:]...)
				break
			}
		}
		s.PlayerManager.PlayersMutex.Unlock()
		s.OnLog("info", fmt.Sprintf("客户端断开: %s", sessionID))
		s.OnPlayerInfoUpdated()
	}
	s.tcpService.AddRoute(proto.ID_AuthenticationIDPackageID, func(ctx easytcp.Context) {
		uploadBytesSize := new(int64)
		downloadBytesSize := new(int64)
		*uploadBytesSize = 0
		var reqData proto.AuthenticationIDPackage
		rawData := ctx.Request().Data()
		*downloadBytesSize = int64(len(rawData) + 8)
		sessionID := ctx.Session().ID().(string)
		defer s.UpdatePlayerTrafficBySessionID(sessionID, uploadBytesSize, downloadBytesSize)
		err := ctx.Bind(&reqData)
		if err != nil {
			s.OnLog("error", "解析认证包失败: "+err.Error())
			return
		}
		if reqData.AuthenticationId == "" || reqData.DeviceId == "" {
			authenticationID, deviceID := s.OnNeedGenerateAuthentication(sessionID)
			req := &proto.ConfirmRegisterPackage{
				AuthenticationId: authenticationID,
				DeviceId:         deviceID,
			}
			sendBytesSize, err := s.tcpService.SendToSession(sessionID, proto.ID_ConfirmRegisterPackageID, req)
			if err != nil {
				s.OnLog("error", "发送确认注册包失败: "+err.Error())
				return
			}
			*uploadBytesSize = *uploadBytesSize + int64(sendBytesSize)
			// 为设备分配端口并启动UDP中继
			port, err := s.allocatePortAndStartRelay(deviceID)
			if err != nil {
				s.OnLog("error", fmt.Sprintf("为设备 %s 分配端口失败: %v", deviceID, err))
				return
			}

			s.PlayerManager.PlayersMutex.Lock()
			defer s.PlayerManager.PlayersMutex.Unlock()
			req1 := &proto.AllUserInfoPackage{
				UserDataList: []*proto.AllUserInfoPackage_UserData{},
			}
			for _, player := range s.PlayerManager.Players {
				if player.SessionID == sessionID {
					player.DeviceID = deviceID
					player.Port = port
					player.Remark = s.GetDeviceRemark(deviceID)
					s.OnPlayerInfoUpdated()
				}
				if player.DeviceID != "" {
					req1.UserDataList = append(req1.UserDataList, &proto.AllUserInfoPackage_UserData{
						DeviceId: player.DeviceID,
						Port:     int32(player.Port),
					})
				}
			}
			packedMsg, err := s.tcpService.PackerData(proto.ID_AllUserInfoPackageID, req1)
			if err != nil {
				s.OnLog("error", "打包用户信息包失败: "+err.Error())
				return
			}
			sendBytesSize = len(packedMsg)
			for index, player := range s.PlayerManager.Players {
				if player.DeviceID != "" && index != 0 {
					err := s.tcpService.SendRawToSession(player.SessionID, packedMsg)
					if err != nil {
						s.OnLog("error", "发送用户信息包失败: "+err.Error())
						continue
					}
					s.PlayerManager.Players[0].TotalUpload += int64(sendBytesSize)
					player.TotalUpload += int64(sendBytesSize)
				}
			}
		} else {
			if s.CheckDeviceAuthentication(int(reqData.Index), reqData.DeviceId, reqData.AuthenticationId) {
				// 为设备分配端口并启动UDP中继
				port, err := s.allocatePortAndStartRelay(reqData.DeviceId)
				if err != nil {
					s.OnLog("error", fmt.Sprintf("为设备 %s 分配端口失败: %v", reqData.DeviceId, err))
					return
				}
				s.PlayerManager.PlayersMutex.Lock()
				defer s.PlayerManager.PlayersMutex.Unlock()
				req := &proto.AllUserInfoPackage{
					UserDataList: []*proto.AllUserInfoPackage_UserData{},
				}
				for _, player := range s.PlayerManager.Players {
					if player.SessionID == sessionID {
						player.DeviceID = reqData.DeviceId
						player.Port = port
						player.Remark = s.GetDeviceRemark(reqData.DeviceId)
						s.OnPlayerInfoUpdated()
					}
					if player.DeviceID != "" {
						req.UserDataList = append(req.UserDataList, &proto.AllUserInfoPackage_UserData{
							DeviceId: player.DeviceID,
							Port:     int32(player.Port),
						})
					}
				}
				packedMsg, err := s.tcpService.PackerData(proto.ID_AllUserInfoPackageID, req)
				if err != nil {
					s.OnLog("error", "打包用户信息包失败: "+err.Error())
					return
				}
				sendBytesSize := len(packedMsg)
				for index, player := range s.PlayerManager.Players {
					if player.DeviceID != "" && index != 0 {
						err := s.tcpService.SendRawToSession(player.SessionID, packedMsg)
						if err != nil {
							s.OnLog("error", "发送用户信息包失败: "+err.Error())
							continue
						}
						s.PlayerManager.Players[0].TotalUpload += int64(sendBytesSize)
						player.TotalUpload += int64(sendBytesSize)
					}
				}
				return
			}
			serviceID, publicKey, err := s.GetKeyPairByServiceIDIndex(int(reqData.Index) + 1)
			if err != nil {
				s.OnLog("error", fmt.Sprintf("获取密钥对失败: %v", err))
				return
			}
			req := &proto.ServiceIDPackage{
				Index:     reqData.Index + 1,
				ServiceId: serviceID,
				PublicKey: publicKey,
			}
			sendBytesSize, err := s.tcpService.SendToSession(sessionID, proto.ID_ServiceIDPackageID, req)
			if err != nil {
				s.OnLog("error", "发送密钥对失败: "+err.Error())
				return
			}
			*uploadBytesSize = int64(sendBytesSize)
		}
	})
	s.tcpService.AddRoute(proto.ID_ForwardPackageID, func(ctx easytcp.Context) {
		uploadBytesSize := new(int64)
		downloadBytesSize := new(int64)
		*uploadBytesSize = 0
		var reqData proto.ForwardPackage
		rawData := ctx.Request().Data()
		*downloadBytesSize = int64(len(rawData) + 8)
		sessionID := ctx.Session().ID().(string)
		defer s.UpdatePlayerTrafficBySessionID(sessionID, uploadBytesSize, downloadBytesSize)
		err := ctx.Bind(&reqData)
		if err != nil {
			s.OnLog("error", "解析转发包失败: "+err.Error())
			return
		}
		s.PlayerManager.PlayersMutex.Lock()
		defer s.PlayerManager.PlayersMutex.Unlock()
		port := 0
		var sendPlayer *data.Player
		receiveSessionID := ""
		for index, player := range s.PlayerManager.Players {
			if index == 0 {
				if player.Port == int(reqData.Port) {
					port = player.Port
				}
			} else {
				if port == 0 {
					if player.Port == int(reqData.Port) {
						receiveSessionID = player.SessionID
					}
					if player.SessionID == sessionID {
						sendPlayer = player
					}
					if sendPlayer != nil && receiveSessionID != "" {
						req := &proto.ForwardPackage{
							Port:  int32(sendPlayer.Port),
							Bytes: reqData.Bytes,
						}
						sendBytesSize, err := s.tcpService.SendToSession(receiveSessionID, proto.ID_ForwardPackageID, req)
						if err != nil {
							s.OnLog("error", "发送转发包失败: "+err.Error())
							return
						}
						player.TotalUpload += int64(sendBytesSize)
						s.PlayerManager.Players[0].TotalUpload += int64(sendBytesSize)
						return
					}
				} else {
					if player.SessionID == sessionID {
						s.udpRelay[player.Port].Send(reqData.Bytes, "127.0.0.1:"+strconv.Itoa(port))
						return
					}
				}
			}
		}

	})
	s.tcpService.AddRoute(proto.ID_BindPortPackageID, func(ctx easytcp.Context) {
		uploadBytesSize := new(int64)
		downloadBytesSize := new(int64)
		*uploadBytesSize = 0
		var reqData proto.ForwardPackage
		rawData := ctx.Request().Data()
		*downloadBytesSize = int64(len(rawData) + 8)
		sessionID := ctx.Session().ID().(string)
		defer s.UpdatePlayerTrafficBySessionID(sessionID, uploadBytesSize, downloadBytesSize)
		err := ctx.Bind(&reqData)
		if err != nil {
			s.OnLog("error", "解析绑定端口包失败: "+err.Error())
			return
		}

		s.PlayerManager.PlayersMutex.Lock()
		defer s.PlayerManager.PlayersMutex.Unlock()
		req := &proto.AllUserInfoPackage{
			UserDataList: []*proto.AllUserInfoPackage_UserData{},
		}
		for _, player := range s.PlayerManager.Players {
			if player.SessionID == sessionID {
				player.Port = int(reqData.Port)
				s.reservePort[player.DeviceID] = player.Port
				if _, ok := s.udpRelay[player.Port]; !ok {
					err := s.startUDPRelay(player.Port)
					if err != nil {
						s.OnLog("error", "启动UDP中继失败: "+err.Error())
					} else {
						s.OnLog("info", fmt.Sprintf("为设备 %s 分配端口 %d，启动UDP中继", sessionID, player.Port))
					}
				}
				s.OnPlayerInfoUpdated()
			}
			if player.DeviceID != "" {
				req.UserDataList = append(req.UserDataList, &proto.AllUserInfoPackage_UserData{
					DeviceId: player.DeviceID,
					Port:     int32(player.Port),
				})
			}
		}
		packedMsg, err := s.tcpService.PackerData(proto.ID_AllUserInfoPackageID, req)
		if err != nil {
			s.OnLog("error", "打包用户信息包失败: "+err.Error())
			return
		}
		sendBytesSize := len(packedMsg)
		for index, player := range s.PlayerManager.Players {
			if player.DeviceID != "" && index != 0 {
				err := s.tcpService.SendRawToSession(player.SessionID, packedMsg)
				if err != nil {
					s.OnLog("error", "发送用户信息包失败: "+err.Error())
					continue
				}
				s.PlayerManager.Players[0].TotalUpload += int64(sendBytesSize)
				player.TotalUpload += int64(sendBytesSize)
			}
		}
	})
	err := s.tcpService.Start(address)
	if err != nil {
		return err
	}
	s.PlayerManager.PlayersMutex.Lock()
	player := data.NewPlayer()
	player.DeviceID = "A"
	player.Port = s.targetPort
	s.PlayerManager.Players = []*data.Player{
		player,
	}
	s.PlayerManager.PlayersMutex.Unlock()
	return nil
}

// 停止服务
func (s *HostSession) Stop() {
	// 停止所有UDP中继服务器
	for port, relay := range s.udpRelay {
		relay.Stop()
		delete(s.udpRelay, port)
	}
	s.tcpService.Stop()
	// 清空端口分配记录
	s.reservePort = make(map[string]int)
	s.PlayerManager.PlayersMutex.Lock()
	s.PlayerManager.Players = []*data.Player{}
	s.PlayerManager.PlayersMutex.Unlock()
}

// 根据sessionID更新玩家流量统计
func (s *HostSession) UpdatePlayerTrafficBySessionID(sessionID string, uploadBytes *int64, downloadBytes *int64) {
	if *uploadBytes == 0 && *downloadBytes == 0 {
		return
	}

	s.PlayerManager.PlayersMutex.Lock()
	defer s.PlayerManager.PlayersMutex.Unlock()

	for index, player := range s.PlayerManager.Players {
		if index == 0 || player.SessionID == sessionID {
			player.TotalUpload += *uploadBytes
			player.TotalDownload += *downloadBytes
		}
	}
}

// 分配端口并启动UDP中继服务器
func (s *HostSession) allocatePortAndStartRelay(deviceID string) (int, error) {
	// 检查是否已为该设备分配端口
	if port, exists := s.reservePort[deviceID]; exists {
		// 检查UDP中继是否还在运行
		if relay, ok := s.udpRelay[port]; ok && relay.IsRunning() {
			return port, nil
		}
		// 如果中继已停止，删除记录重新分配
		delete(s.reservePort, deviceID)
		delete(s.udpRelay, port)
	}

	// 查找可用端口（从游戏端口+1开始）
	startPort := s.targetPort + 1
	for port := startPort; port < startPort+100; port++ {
		if _, exists := s.udpRelay[port]; !exists {
			err := s.startUDPRelay(port)
			if err != nil {
				// 端口可能被占用，尝试下一个
				continue
			}

			// 记录分配的端口和UDP中继实例
			s.reservePort[deviceID] = port

			s.OnLog("info", fmt.Sprintf("为设备 %s 分配端口 %d，启动UDP中继", deviceID, port))
			return port, nil
		}
	}

	return 0, errors.New("无法找到可用端口")
}

// 根据指定端口在TCP客户端侧启动UDP中继
func (s *HostSession) startUDPRelay(port int) error {
	// 检查是否已存在该端口的UDP中继
	if _, exists := s.udpRelay[port]; exists {
		return nil // 已存在，无需重复启动
	}

	udpConfig := udp.DefaultConfig()
	udpConfig.Port = port

	// 创建UDP中继服务
	relay := udp.NewUDPService(udpConfig)
	relay.SetLogCallback(s.OnLog)
	// 设置数据回调函数
	relay.SetDataCallback(func(data []byte, addr *net.UDPAddr) {
		s.PlayerManager.PlayersMutex.Lock()
		defer s.PlayerManager.PlayersMutex.Unlock()
		for index, player := range s.PlayerManager.Players {
			if index == 0 {
				if player.Port != addr.Port {

				}
			} else if player.Port == udpConfig.Port {
				req := &proto.ForwardPackage{
					Port:  int32(addr.Port),
					Bytes: data,
				}
				sendBytesSize, err := s.tcpService.SendToSession(player.SessionID, proto.ID_ForwardPackageID, req)
				if err != nil {
					s.OnLog("error", "发送转发包失败: "+err.Error())
					return
				}
				player.TotalUpload += int64(sendBytesSize)
				s.PlayerManager.Players[0].TotalUpload += int64(sendBytesSize)
				break
			}
		}
	})

	// 启动UDP中继
	err := relay.Start()
	if err != nil {
		return err
	}

	// 保存到管理映射
	s.udpRelay[port] = relay
	return nil
}
