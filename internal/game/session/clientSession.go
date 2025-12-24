package session

import (
	"encoding/base64"
	"fmt"
	"net"
	"strconv"

	"github.com/1143910315/UDPRelayServer/internal/game/data"
	"github.com/1143910315/UDPRelayServer/internal/network/tcp"
	"github.com/1143910315/UDPRelayServer/internal/network/udp"
	"github.com/1143910315/UDPRelayServer/internal/proto"
	"github.com/1143910315/UDPRelayServer/internal/security"
	"github.com/DarthPestilane/easytcp"
)

type ClientSession struct {
	tcpClient              *tcp.TCPClient
	serviceID              string
	udpRelay               map[int]*udp.UDPService
	PlayerManager          *data.PlayerManager
	OnLog                  func(level, message string)
	OnPlayerInfoUpdated    func()
	GetDeviceServiceRemark func(deviceID string, serviceID string) string
	GetServerByServiceID   func(serviceID string) (authCode string, deviceID string, err error)
	InsertServer           func(serviceID string, authCode string, deviceID string) error
}

func NewClientSession() *ClientSession {
	return &ClientSession{
		udpRelay:      map[int]*udp.UDPService{},
		PlayerManager: data.NewPlayerManager(),
	}
}

func (s *ClientSession) Connect(addr string) error {
	s.tcpClient = tcp.NewTCPClient()
	s.tcpClient.OnLog = s.OnLog
	s.tcpClient.OnConnected = func() {
		s.PlayerManager.PlayersMutex.Lock()
		player := data.NewPlayer()
		s.PlayerManager.Players = []*data.Player{
			player,
		}
		s.PlayerManager.PlayersMutex.Unlock()
		s.OnLog("info", fmt.Sprintf("连接到服务器 %s", addr))
	}
	s.tcpClient.OnDisconnected = func() {
		s.OnLog("info", "与服务器断开连接")
		s.PlayerManager.PlayersMutex.Lock()
		s.PlayerManager.Players = make([]*data.Player, 0)
		s.PlayerManager.PlayersMutex.Unlock()
		s.OnPlayerInfoUpdated()
	}
	s.tcpClient.AddHandler(proto.ID_ServiceIDPackageID, func(m *easytcp.Message) {
		uploadBytesSize := new(int64)
		downloadBytesSize := new(int64)
		*uploadBytesSize = 0
		mData := m.Data()
		*downloadBytesSize = int64(len(mData) + 8)
		defer s.UpdatePlayerTrafficByDeviceID("A", uploadBytesSize, downloadBytesSize)
		var respData proto.ServiceIDPackage
		if err := s.tcpClient.Codec.Decode(mData, &respData); err != nil {
			s.OnLog("error", fmt.Sprintf("解析服务ID包失败: %v", err))
			return
		}

		s.serviceID = respData.ServiceId
		authCode, deviceID, err := s.GetServerByServiceID(respData.ServiceId)
		authenticationID := ""
		if err != nil {
			s.OnLog("info", fmt.Sprintf("连接到新服务器：%s", respData.ServiceId))
		} else {
			encryptedAuthCode, err := security.EncryptWithPublicKey(respData.PublicKey, []byte(authCode))
			if err != nil {
				s.OnLog("error", fmt.Sprintf("加密认证码失败: %v", err))
				deviceID = ""
			} else {
				authenticationID = base64.StdEncoding.EncodeToString(encryptedAuthCode)
			}
		}

		req := &proto.AuthenticationIDPackage{
			Index:            0,
			DeviceId:         deviceID,
			AuthenticationId: authenticationID,
		}
		sendBytesSize, err := s.tcpClient.Send(proto.ID_AuthenticationIDPackageID, req)
		if err != nil {
			s.OnLog("error", fmt.Sprintf("发送认证码失败: %v", err))
			return
		}
		*uploadBytesSize = int64(sendBytesSize)
		s.PlayerManager.PlayersMutex.Lock()
		s.PlayerManager.Players[0].DeviceID = deviceID
		s.PlayerManager.Players[0].Remark = s.GetDeviceServiceRemark(deviceID, s.serviceID)
		s.PlayerManager.PlayersMutex.Unlock()
		s.OnPlayerInfoUpdated()
	})
	s.tcpClient.AddHandler(proto.ID_ConfirmRegisterPackageID, func(m *easytcp.Message) {
		uploadBytesSize := new(int64)
		downloadBytesSize := new(int64)
		*uploadBytesSize = 0
		mData := m.Data()
		*downloadBytesSize = int64(len(mData) + 8)
		defer s.UpdatePlayerTrafficByDeviceID("A", uploadBytesSize, downloadBytesSize)
		var respData proto.ConfirmRegisterPackage
		if err := s.tcpClient.Codec.Decode(mData, &respData); err != nil {
			s.OnLog("error", fmt.Sprintf("解析确认注册包失败: %v", err))
			return
		}

		s.InsertServer(s.serviceID, respData.AuthenticationId, respData.DeviceId)
		s.PlayerManager.PlayersMutex.Lock()
		s.PlayerManager.Players[0].DeviceID = respData.DeviceId
		s.PlayerManager.Players[0].Remark = s.GetDeviceServiceRemark(respData.DeviceId, s.serviceID)
		s.PlayerManager.PlayersMutex.Unlock()
		s.OnPlayerInfoUpdated()
	})
	s.tcpClient.AddHandler(proto.ID_AllUserInfoPackageID, func(m *easytcp.Message) {
		uploadBytesSize := new(int64)
		downloadBytesSize := new(int64)
		*uploadBytesSize = 0
		mData := m.Data()
		*downloadBytesSize = int64(len(mData) + 8)
		defer s.UpdatePlayerTrafficByDeviceID("A", uploadBytesSize, downloadBytesSize)
		var respData proto.AllUserInfoPackage
		if err := s.tcpClient.Codec.Decode(mData, &respData); err != nil {
			s.OnLog("error", fmt.Sprintf("解析所有玩家信息包失败: %v", err))
			return
		}

		// 根据服务器发送的玩家数据更新本地玩家列表
		s.PlayerManager.PlayersMutex.Lock()

		// 获取本地玩家的设备ID
		localDeviceID := ""
		if len(s.PlayerManager.Players) > 0 {
			localDeviceID = s.PlayerManager.Players[0].DeviceID
		}

		// 创建设备ID到玩家数据的映射，便于查找
		serverPlayers := make(map[string]*proto.AllUserInfoPackage_UserData)
		for _, userData := range respData.UserDataList {
			// 跳过本地玩家，避免重复添加
			if userData.DeviceId == localDeviceID {
				s.PlayerManager.Players[0].Port = int(userData.Port)
				if udpRelay, ok := s.udpRelay[s.PlayerManager.Players[0].Port]; ok {
					udpRelay.Stop()
					delete(s.udpRelay, s.PlayerManager.Players[0].Port)
				}
				continue
			}
			serverPlayers[userData.DeviceId] = userData
		}

		// 处理本地玩家列表：删除、修改、新增
		var updatedPlayers []*data.Player

		// 首先保留本地玩家（索引0）
		if len(s.PlayerManager.Players) > 0 {
			localPlayer := s.PlayerManager.Players[0]
			updatedPlayers = append(updatedPlayers, localPlayer)
		}

		// 处理服务器返回的玩家数据
		for deviceID, serverPlayer := range serverPlayers {
			// 查找是否已存在该玩家
			found := false
			for i := 1; i < len(s.PlayerManager.Players); i++ { // 从1开始，跳过本地玩家
				if s.PlayerManager.Players[i].DeviceID == deviceID {
					// 更新现有玩家信息
					s.PlayerManager.Players[i].Port = int(serverPlayer.Port)
					s.startUDPRelay(s.PlayerManager.Players[i].Port)
					updatedPlayers = append(updatedPlayers, s.PlayerManager.Players[i])
					found = true
					break
				}
			}

			if !found {
				player := data.NewPlayer()
				player.DeviceID = deviceID
				player.Port = int(serverPlayer.Port)
				player.Remark = s.GetDeviceServiceRemark(deviceID, s.serviceID)
				if _, ok := s.udpRelay[player.Port]; !ok {
					err := s.startUDPRelay(player.Port)
					if err != nil {
						s.OnLog("error", fmt.Sprintf("启动端口 %d 的UDP中继失败: %v", player.Port, err))
					}
				}
				updatedPlayers = append(updatedPlayers, player)
			}
		}

		// 更新玩家列表
		s.PlayerManager.Players = updatedPlayers
		s.PlayerManager.PlayersMutex.Unlock()
		s.OnPlayerInfoUpdated()
	})
	s.tcpClient.AddHandler(proto.ID_ForwardPackageID, func(m *easytcp.Message) {
		s.PlayerManager.PlayersMutex.Lock()
		deviceID := "A"
		port := 0
		targetPort := 0
		mData := m.Data()
		var respData proto.ForwardPackage
		if err := s.tcpClient.Codec.Decode(mData, &respData); err != nil {
			s.OnLog("error", fmt.Sprintf("解析转发包失败: %v", err))
			return
		}
		for index, player := range s.PlayerManager.Players {
			if index == 0 {
				targetPort = player.Port
			} else if player.Port == int(respData.Port) {
				deviceID = player.DeviceID
				port = player.Port
				break
			}
		}
		s.PlayerManager.PlayersMutex.Unlock()

		uploadBytesSize := new(int64)
		downloadBytesSize := new(int64)
		*uploadBytesSize = 0
		*downloadBytesSize = int64(len(mData) + 8)
		defer s.UpdatePlayerTrafficByDeviceID(deviceID, uploadBytesSize, downloadBytesSize)

		if udpRelay, exists := s.udpRelay[port]; exists {
			err := udpRelay.Send(respData.Bytes, "127.0.0.1:"+strconv.Itoa(targetPort))
			if err != nil {
				s.OnLog("error", fmt.Sprintf("发送UDP数据失败: %v", err))
				return
			}
			return
		}
		s.OnLog("error", fmt.Sprintf("未找到端口 %d 的UDP中继", port))
	})
	s.tcpClient.AddHandler(proto.ID_AddOrUpdateDevicePackageID, func(m *easytcp.Message) {
		uploadBytesSize := new(int64)
		downloadBytesSize := new(int64)
		*uploadBytesSize = 0
		mData := m.Data()
		*downloadBytesSize = int64(len(mData) + 8)
		defer s.UpdatePlayerTrafficByDeviceID("A", uploadBytesSize, downloadBytesSize)
		var respData proto.AddOrUpdateDevicePackage
		if err := s.tcpClient.Codec.Decode(mData, &respData); err != nil {
			s.OnLog("error", fmt.Sprintf("解析添加或更新设备包失败: %v", err))
			return
		}

		s.PlayerManager.PlayersMutex.Lock()
		defer s.PlayerManager.PlayersMutex.Unlock()
		s.startUDPRelay(int(respData.Port))
		for _, player := range s.PlayerManager.Players {
			if player.DeviceID == respData.DeviceId {
				player.Port = int(respData.Port)
				return
			}
		}
		player := data.NewPlayer()
		player.DeviceID = respData.DeviceId
		player.Port = int(respData.Port)
		player.Remark = s.GetDeviceServiceRemark(respData.DeviceId, s.serviceID)
		s.PlayerManager.Players = append(s.PlayerManager.Players, player)
		s.OnPlayerInfoUpdated()
	})
	s.tcpClient.AddHandler(proto.ID_RemoveDevicePackageID, func(m *easytcp.Message) {
		uploadBytesSize := new(int64)
		downloadBytesSize := new(int64)
		*uploadBytesSize = 0
		mData := m.Data()
		*downloadBytesSize = int64(len(mData) + 8)
		defer s.UpdatePlayerTrafficByDeviceID("A", uploadBytesSize, downloadBytesSize)
		var respData proto.RemoveDevicePackage
		if err := s.tcpClient.Codec.Decode(mData, &respData); err != nil {
			s.OnLog("error", fmt.Sprintf("解析删除设备包失败: %v", err))
			return
		}

		s.PlayerManager.PlayersMutex.Lock()
		defer s.PlayerManager.PlayersMutex.Unlock()
		for index, player := range s.PlayerManager.Players {
			if player.DeviceID == respData.DeviceId {
				s.PlayerManager.Players = append(s.PlayerManager.Players[:index], s.PlayerManager.Players[index+1:]...)
			}
		}
	})
	return s.tcpClient.Connect(addr)
}

func (s *ClientSession) Disconnect() {
	// 停止所有UDP中继服务器
	for port, relay := range s.udpRelay {
		relay.Stop()
		delete(s.udpRelay, port)
	}
	s.tcpClient.Disconnect()
	s.PlayerManager.PlayersMutex.Lock()
	s.PlayerManager.Players = make([]*data.Player, 0)
	s.PlayerManager.PlayersMutex.Unlock()
}

// 根据DeviceID更新玩家流量统计
func (s *ClientSession) UpdatePlayerTrafficByDeviceID(deviceID string, uploadBytes *int64, downloadBytes *int64) {
	if *uploadBytes == 0 && *downloadBytes == 0 {
		return
	}

	s.PlayerManager.PlayersMutex.Lock()
	defer s.PlayerManager.PlayersMutex.Unlock()

	for index, player := range s.PlayerManager.Players {
		if index == 0 || player.DeviceID == deviceID {
			player.TotalUpload += *uploadBytes
			player.TotalDownload += *downloadBytes
		}
	}
}

// 根据指定端口在TCP客户端侧启动UDP中继
func (s *ClientSession) startUDPRelay(port int) error {
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
		uploadBytesSize := new(int64)
		downloadBytesSize := new(int64)
		*uploadBytesSize = 0
		*downloadBytesSize = 0
		defer s.UpdatePlayerTrafficByDeviceID("A", uploadBytesSize, downloadBytesSize)

		s.PlayerManager.PlayersMutex.Lock()
		defer s.PlayerManager.PlayersMutex.Unlock()

		// 找到对应端口的玩家并转发数据
		for index, player := range s.PlayerManager.Players {
			if index == 0 {
				if player.Port != addr.Port {
					s.OnLog("info", fmt.Sprintf("从端口 %d 切换到端口 %d", player.Port, addr.Port))
					player.Port = addr.Port
					req := &proto.BindPortPackage{
						Port: int32(player.Port),
					}
					sendBytesSize, err := s.tcpClient.Send(proto.ID_BindPortPackageID, req)
					if err != nil {
						s.OnLog("error", fmt.Sprintf("发送绑定端口失败: %v", err))
						return
					}
					*uploadBytesSize = *uploadBytesSize + int64(sendBytesSize)
				}
			} else if player.Port == udpConfig.Port {
				// 创建转发包
				req := &proto.ForwardPackage{
					Port:  int32(udpConfig.Port),
					Bytes: data,
				}
				sendBytesSize, err := s.tcpClient.Send(proto.ID_ForwardPackageID, req)
				if err != nil {
					s.OnLog("error", fmt.Sprintf("转发数据给 %d 失败: %v", udpConfig.Port, err))
					return
				}

				// 更新流量统计
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
	s.OnLog("info", fmt.Sprintf("在端口 %d 启动UDP中继", port))

	return nil
}
