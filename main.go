package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/1143910315/UDPRelayServer/internal/config"
	"github.com/1143910315/UDPRelayServer/internal/game"
	"github.com/1143910315/UDPRelayServer/internal/gui"
	"github.com/1143910315/UDPRelayServer/internal/gui/ui"
	"github.com/1143910315/UDPRelayServer/internal/network/tcp"
	"github.com/1143910315/UDPRelayServer/internal/network/udp"
	"github.com/1143910315/UDPRelayServer/internal/proto"
	"github.com/1143910315/UDPRelayServer/internal/security"
	"github.com/1143910315/UDPRelayServer/internal/utils"
	"github.com/DarthPestilane/easytcp"
	"github.com/google/uuid"
)

// GUI应用程序结构
type UDPRelayApp struct {
	app                       fyne.App
	mainPage                  *ui.MainPage
	tcpService                *tcp.TCPServer
	tcpClient                 *tcp.TCPClient
	configManager             *config.ConfigManager
	playerManager             *game.PlayerManager
	advancedStringIncrementer *utils.AdvancedStringIncrementer
	serviceID                 string
	listenPort                int
	targetPort                int
	reservePort               map[string]int
	udpRelay                  map[int]*udp.UDPService
}

// NewUDPRelayApp 创建新的GUI应用程序
func NewUDPRelayApp() (*UDPRelayApp, error) {
	myApp := app.New()
	// 设置自定义主题
	myApp.Settings().SetTheme(&gui.CustomTheme{})

	// 初始化配置管理器
	configManager, err := config.NewConfigManager()
	if err != nil {
		return nil, err
	}

	return &UDPRelayApp{
		app:                       myApp,
		mainPage:                  ui.NewMainPage(myApp),
		configManager:             configManager,
		playerManager:             game.NewPlayerManager(configManager),
		advancedStringIncrementer: utils.NewAdvancedStringIncrementer(),
		reservePort:               make(map[string]int),
		udpRelay:                  make(map[int]*udp.UDPService),
	}, nil
}

func (ua *UDPRelayApp) initData() {
	options, err := ua.configManager.GetAllConnectionAddresses()
	if err != nil {
		ua.appendLog(fmt.Sprintf("获取连接地址失败: %v", err))
	}
	if options == nil {
		options = []string{
			"127.0.0.1:8080",
		}
	}
	ua.mainPage.HistoryAddressSelect.Options = options

	ua.mainPage.ListenPortEntry.SetText(ua.configManager.GetConfig("listen_port", "8080"))
	ua.mainPage.TargetPortEntry.SetText(ua.configManager.GetConfig("target_port", "8081"))
	lastConnectionAddress := ua.configManager.GetLastConnectionAddress("127.0.0.1:8080")
	ua.mainPage.TargetHostEntry.SetText(lastConnectionAddress)
	ua.mainPage.HistoryAddressSelect.SetSelected(lastConnectionAddress)

	lastTab := ua.configManager.GetConfig("last_tab", "0")
	if tabIndex, err := strconv.Atoi(lastTab); err == nil && tabIndex >= 0 && tabIndex < len(ua.mainPage.Tabs.Items) {
		ua.mainPage.Tabs.SelectIndex(tabIndex)
	}
}

// 设置事件回调
func (ua *UDPRelayApp) setEventCallbacks() {
	ua.mainPage.ListenPortEntry.OnChanged = func(s string) {
		ua.configManager.SetConfigDebounced("listen_port", s)
	}
	ua.mainPage.TargetPortEntry.OnChanged = func(s string) {
		ua.configManager.SetConfigDebounced("target_port", s)
	}
	ua.mainPage.HistoryAddressSelect.OnChanged = func(s string) {
		ua.mainPage.TargetHostEntry.SetText(s)
	}
	ua.mainPage.StartBtn.OnTapped = func() {
		listenPortText := strings.TrimSpace(ua.mainPage.ListenPortEntry.Text)
		listenPort, err := strconv.Atoi(listenPortText)
		if err != nil || listenPort < 1 || listenPort > 65535 {
			ua.appendLog("错误: 监听端口必须是 1-65535 之间的数字")
			return
		}
		targetPort, err := strconv.Atoi(strings.TrimSpace(ua.mainPage.TargetPortEntry.Text))
		if err != nil || targetPort < 1 || targetPort > 65535 {
			ua.appendLog("错误: 游戏端口必须是 1-65535 之间的数字")
			return
		}
		ua.listenPort = listenPort
		ua.targetPort = targetPort

		ua.tcpService = tcp.NewTCPServer()
		ua.tcpService.OnClientConnected = func(sessionID string) {
			uploadBytesSize := new(int64)
			downloadBytesSize := new(int64)
			*uploadBytesSize = 0
			*downloadBytesSize = 0
			defer ua.UpdatePlayerTrafficBySessionID(sessionID, uploadBytesSize, downloadBytesSize)

			player := game.NewPlayer()
			player.SessionID = sessionID
			ua.playerManager.PlayersMutex.Lock()
			ua.playerManager.Players = append(ua.playerManager.Players, player)
			ua.playerManager.PlayersMutex.Unlock()

			fyne.Do(func() {
				ua.appendLog(fmt.Sprintf("客户端连接: %s", sessionID))
				ua.mainPage.RefreshPlayerTable() // 刷新表格显示
			})
			serviceID, _, publicKey, err := ua.configManager.GetKeyPairByID(0)
			if err != nil {
				fyne.Do(func() {
					ua.appendLog(fmt.Sprintf("获取密钥对失败: %v", err))
				})
				return
			}
			req := &proto.ServiceIDPackage{
				Index:     0,
				ServiceId: serviceID,
				PublicKey: publicKey,
			}
			sendBytesSize, err := ua.tcpService.SendToSession(sessionID, proto.ID_ServiceIDPackageID, req)
			if err != nil {
				fyne.Do(func() {
					ua.appendLog(fmt.Sprintf("发送密钥对失败: %v", err))
				})
				return
			}
			*uploadBytesSize = int64(sendBytesSize)
		}
		ua.tcpService.OnClientDisconnected = func(sessionID string) {
			ua.playerManager.PlayersMutex.Lock()
			for i, player := range ua.playerManager.Players {
				if player.SessionID == sessionID {
					ua.playerManager.Players = append(ua.playerManager.Players[:i], ua.playerManager.Players[i+1:]...)
					break
				}
			}
			ua.playerManager.PlayersMutex.Unlock()
			fyne.Do(func() {
				ua.appendLog(fmt.Sprintf("客户端断开: %s", sessionID))
				ua.mainPage.RefreshPlayerTable() // 刷新表格显示
			})
		}
		ua.tcpService.AddRoute(proto.ID_AuthenticationIDPackageID, func(ctx easytcp.Context) {
			uploadBytesSize := new(int64)
			downloadBytesSize := new(int64)
			*uploadBytesSize = 0
			var reqData proto.AuthenticationIDPackage
			rawData := ctx.Request().Data()
			*downloadBytesSize = int64(len(rawData) + 8)
			sessionID := ctx.Session().ID().(string)
			defer ua.UpdatePlayerTrafficBySessionID(sessionID, uploadBytesSize, downloadBytesSize)
			err := ctx.Bind(&reqData)
			if err != nil {
				fyne.Do(func() {
					ua.appendLog(fmt.Sprintf("解析认证包失败: %v", err))
				})
				return
			}
			if reqData.AuthenticationId == "" || reqData.DeviceId == "" {
				nextDeviceID := ua.advancedStringIncrementer.IncrementString(ua.configManager.GetConfig("max_generated_device_id", "A"))
				ua.configManager.SetConfig("max_generated_device_id", nextDeviceID)
				uuidObj, _ := uuid.NewUUID()
				authenticationID := uuidObj.String()
				ua.configManager.SetDeviceAuthentication(nextDeviceID, authenticationID)
				req := &proto.ConfirmRegisterPackage{
					AuthenticationId: authenticationID,
					DeviceId:         nextDeviceID,
				}
				sendBytesSize, err := ua.tcpService.SendToSession(sessionID, proto.ID_ConfirmRegisterPackageID, req)
				if err != nil {
					fyne.Do(func() {
						ua.appendLog(fmt.Sprintf("发送密钥对失败: %v", err))
					})
					return
				}
				*uploadBytesSize = *uploadBytesSize + int64(sendBytesSize)
				// 为设备分配端口并启动UDP中继
				port, err := ua.allocatePortAndStartRelay(reqData.DeviceId)
				if err != nil {
					fyne.Do(func() {
						ua.appendLog(fmt.Sprintf("为设备 %s 分配端口失败: %v", reqData.DeviceId, err))
					})
					return
				}

				ua.playerManager.PlayersMutex.Lock()
				defer ua.playerManager.PlayersMutex.Unlock()
				req1 := &proto.AllUserInfoPackage{
					UserDataList: []*proto.AllUserInfoPackage_UserData{},
				}
				for index, player := range ua.playerManager.Players {
					if player.SessionID == sessionID {
						player.DeviceID = nextDeviceID
						player.Port = port
						remark, err := ua.configManager.GetDeviceServiceRemark(nextDeviceID, "")
						if err == nil {
							player.Remark = remark
						} else {
							player.Remark = ""
						}
						fyne.Do(func() {
							ua.mainPage.RefreshPlayerTableItem(index+1, 0)
							ua.mainPage.RefreshPlayerTableItem(index+1, 1)
						})
					}
					if player.DeviceID != "" {
						req1.UserDataList = append(req1.UserDataList, &proto.AllUserInfoPackage_UserData{
							DeviceId: player.DeviceID,
							Port:     int32(player.Port),
						})
					}
				}
				packedMsg, err := ua.tcpService.PackerData(proto.ID_AllUserInfoPackageID, req1)
				if err != nil {
					fyne.Do(func() {
						ua.appendLog(fmt.Sprintf("打包用户信息包失败: %v", err))
					})
					return
				}
				sendBytesSize = len(packedMsg)
				for index, player := range ua.playerManager.Players {
					if player.DeviceID != "" && index != 0 {
						err := ua.tcpService.SendRawToSession(player.SessionID, packedMsg)
						if err != nil {
							fyne.Do(func() {
								ua.appendLog(fmt.Sprintf("发送用户信息包失败: %v", err))
							})
							continue
						}
						ua.playerManager.Players[0].TotalUpload += int64(sendBytesSize)
						player.TotalUpload += int64(sendBytesSize)
					}
				}
			} else {
				_, privateKey, _, err := ua.configManager.GetKeyPairByID(int(reqData.Index))
				if err == nil {
					encryptedData, err := base64.StdEncoding.DecodeString(reqData.AuthenticationId)
					if err == nil {
						authenticationID, err := security.DecryptWithPrivateKey(privateKey, encryptedData)
						if err == nil {
							authentication, err := ua.configManager.GetDeviceAuthentication(reqData.DeviceId)
							if err == nil && string(authenticationID) == authentication {
								// 为设备分配端口并启动UDP中继
								port, err := ua.allocatePortAndStartRelay(reqData.DeviceId)
								if err != nil {
									fyne.Do(func() {
										ua.appendLog(fmt.Sprintf("为设备 %s 分配端口失败: %v", reqData.DeviceId, err))
									})
									return
								}
								ua.playerManager.PlayersMutex.Lock()
								defer ua.playerManager.PlayersMutex.Unlock()
								req := &proto.AllUserInfoPackage{
									UserDataList: []*proto.AllUserInfoPackage_UserData{},
								}
								for index, player := range ua.playerManager.Players {
									if player.SessionID == sessionID {
										player.DeviceID = reqData.DeviceId
										player.Port = port
										remark, err := ua.configManager.GetDeviceServiceRemark(reqData.DeviceId, "")
										if err == nil {
											player.Remark = remark
										} else {
											player.Remark = ""
										}
										fyne.Do(func() {
											ua.mainPage.RefreshPlayerTableItem(index+1, 0)
											ua.mainPage.RefreshPlayerTableItem(index+1, 1)
										})
									}
									if player.DeviceID != "" {
										req.UserDataList = append(req.UserDataList, &proto.AllUserInfoPackage_UserData{
											DeviceId: player.DeviceID,
											Port:     int32(player.Port),
										})
									}
								}
								packedMsg, err := ua.tcpService.PackerData(proto.ID_AllUserInfoPackageID, req)
								if err != nil {
									fyne.Do(func() {
										ua.appendLog(fmt.Sprintf("打包用户信息包失败: %v", err))
									})
									return
								}
								sendBytesSize := len(packedMsg)
								for index, player := range ua.playerManager.Players {
									if player.DeviceID != "" && index != 0 {
										err := ua.tcpService.SendRawToSession(player.SessionID, packedMsg)
										if err != nil {
											fyne.Do(func() {
												ua.appendLog(fmt.Sprintf("发送用户信息包失败: %v", err))
											})
											continue
										}
										ua.playerManager.Players[0].TotalUpload += int64(sendBytesSize)
										player.TotalUpload += int64(sendBytesSize)
									}
								}
								return
							}
						}
					}
				}
				serviceID, _, publicKey, err := ua.configManager.GetKeyPairByID(int(reqData.Index) + 1)
				if err != nil {
					fyne.Do(func() {
						ua.appendLog(fmt.Sprintf("获取密钥对失败: %v", err))
					})
					return
				}
				req := &proto.ServiceIDPackage{
					Index:     reqData.Index + 1,
					ServiceId: serviceID,
					PublicKey: publicKey,
				}
				sendBytesSize, err := ua.tcpService.SendToSession(sessionID, proto.ID_ServiceIDPackageID, req)
				if err != nil {
					fyne.Do(func() {
						ua.appendLog(fmt.Sprintf("发送密钥对失败: %v", err))
					})
					return
				}
				*uploadBytesSize = int64(sendBytesSize)
			}
		})
		ua.tcpService.AddRoute(proto.ID_ForwardPackageID, func(ctx easytcp.Context) {
			uploadBytesSize := new(int64)
			downloadBytesSize := new(int64)
			*uploadBytesSize = 0
			var reqData proto.ForwardPackage
			rawData := ctx.Request().Data()
			*downloadBytesSize = int64(len(rawData) + 8)
			sessionID := ctx.Session().ID().(string)
			defer ua.UpdatePlayerTrafficBySessionID(sessionID, uploadBytesSize, downloadBytesSize)
			err := ctx.Bind(&reqData)
			if err != nil {
				fyne.Do(func() {
					ua.appendLog(fmt.Sprintf("解析认证包失败: %v", err))
				})
				return
			}
			ua.playerManager.PlayersMutex.Lock()
			defer ua.playerManager.PlayersMutex.Unlock()
			port := 0
			var sendPlayer *game.Player
			receiveSessionID := ""
			for index, player := range ua.playerManager.Players {
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
							sendBytesSize, err := ua.tcpService.SendToSession(receiveSessionID, proto.ID_ForwardPackageID, req)
							if err != nil {
								fyne.Do(func() {
									ua.appendLog(fmt.Sprintf("发送转发包失败: %v", err))
								})
								return
							}
							player.TotalUpload += int64(sendBytesSize)
							ua.playerManager.Players[0].TotalUpload += int64(sendBytesSize)
							return
						}
					} else {
						if player.SessionID == sessionID {
							ua.udpRelay[player.Port].Send(reqData.Bytes, "127.0.0.1:"+strconv.Itoa(port))
							return
						}
					}
				}
			}

		})
		ua.tcpService.AddRoute(proto.ID_BindPortPackageID, func(ctx easytcp.Context) {
			uploadBytesSize := new(int64)
			downloadBytesSize := new(int64)
			*uploadBytesSize = 0
			var reqData proto.ForwardPackage
			rawData := ctx.Request().Data()
			*downloadBytesSize = int64(len(rawData) + 8)
			sessionID := ctx.Session().ID().(string)
			defer ua.UpdatePlayerTrafficBySessionID(sessionID, uploadBytesSize, downloadBytesSize)
			err := ctx.Bind(&reqData)
			if err != nil {
				fyne.Do(func() {
					ua.appendLog(fmt.Sprintf("解析认证包失败: %v", err))
				})
				return
			}

			ua.playerManager.PlayersMutex.Lock()
			defer ua.playerManager.PlayersMutex.Unlock()
			req := &proto.AllUserInfoPackage{
				UserDataList: []*proto.AllUserInfoPackage_UserData{},
			}
			for index, player := range ua.playerManager.Players {
				if player.SessionID == sessionID {
					player.Port = int(reqData.Port)
					ua.reservePort[player.DeviceID] = player.Port
					if _, ok := ua.udpRelay[player.Port]; !ok {
						udpConfig := udp.DefaultConfig()
						udpConfig.Port = player.Port
						relay := udp.NewUDPService(udpConfig)
						relay.SetDataCallback(func(data []byte, addr *net.UDPAddr) {
							ua.playerManager.PlayersMutex.Lock()
							defer ua.playerManager.PlayersMutex.Unlock()
							for index, player := range ua.playerManager.Players {
								if index == 0 {
									if player.Port != addr.Port {

									}
								} else if player.Port == udpConfig.Port {
									req := &proto.ForwardPackage{
										Port:  int32(ua.playerManager.Players[0].Port),
										Bytes: data,
									}
									sendBytesSize, err := ua.tcpService.SendToSession(player.SessionID, proto.ID_ForwardPackageID, req)
									if err != nil {
										fyne.Do(func() {
											ua.appendLog(fmt.Sprintf("发送转发包失败: %v", err))
										})
										return
									}
									player.TotalUpload += int64(sendBytesSize)
									ua.playerManager.Players[0].TotalUpload += int64(sendBytesSize)
									break
								}
							}
						})
						err := relay.Start()
						if err != nil {
							fyne.Do(func() {
								ua.appendLog(fmt.Sprintf("启动UDP中继失败: %v", err))
							})
						}
						ua.udpRelay[player.Port] = relay
					}
					fyne.Do(func() {
						ua.mainPage.RefreshPlayerTableItem(index+1, 1)
					})
				}
				if player.DeviceID != "" {
					req.UserDataList = append(req.UserDataList, &proto.AllUserInfoPackage_UserData{
						DeviceId: player.DeviceID,
						Port:     int32(player.Port),
					})
				}
			}
			packedMsg, err := ua.tcpService.PackerData(proto.ID_AllUserInfoPackageID, req)
			if err != nil {
				fyne.Do(func() {
					ua.appendLog(fmt.Sprintf("打包用户信息包失败: %v", err))
				})
				return
			}
			sendBytesSize := len(packedMsg)
			for index, player := range ua.playerManager.Players {
				if player.DeviceID != "" && index != 0 {
					err := ua.tcpService.SendRawToSession(player.SessionID, packedMsg)
					if err != nil {
						fyne.Do(func() {
							ua.appendLog(fmt.Sprintf("发送用户信息包失败: %v", err))
						})
						continue
					}
					ua.playerManager.Players[0].TotalUpload += int64(sendBytesSize)
					player.TotalUpload += int64(sendBytesSize)
				}
			}
		})
		err = ua.tcpService.Start(":" + listenPortText)
		if err != nil {
			ua.appendLog(fmt.Sprintf("启动TCP服务器失败: %v", err))
			return
		}
		ua.playerManager.PlayersMutex.Lock()
		player := game.NewPlayer()
		player.DeviceID = "A"
		player.Port = ua.targetPort
		ua.playerManager.Players = []*game.Player{
			player,
		}
		ua.playerManager.PlayersMutex.Unlock()
		ua.mainPage.RefreshPlayerTable()

		ua.appendLog(fmt.Sprintf("信息：TCP服务器启动成功，监听 %s", listenPortText))

		ua.mainPage.SetHostStatus()
	}
	ua.mainPage.StopBtn.OnTapped = func() {
		// 停止所有UDP中继服务器
		for port, relay := range ua.udpRelay {
			relay.Stop()
			delete(ua.udpRelay, port)
		}
		// 清空端口分配记录
		ua.reservePort = make(map[string]int)
		ua.playerManager.PlayersMutex.Lock()
		ua.playerManager.Players = []*game.Player{}
		ua.playerManager.PlayersMutex.Unlock()
		ua.mainPage.RefreshPlayerTable()

		if ua.tcpService != nil {
			ua.tcpService.Stop()
			ua.tcpService = nil
		}

		ua.mainPage.SetIdleStatus()
	}
	ua.mainPage.ConnectBtn.OnTapped = func() {
		targetHost := strings.TrimSpace(ua.mainPage.TargetHostEntry.Text)
		if targetHost == "" {
			ua.appendLog("错误：请输入目标主机地址")
			return
		}

		ua.tcpClient = tcp.NewTCPClient()
		ua.tcpClient.OnLog = func(level, message string) {
			fyne.Do(func() {
				switch level {
				case "DEBUG":
					ua.appendLog(fmt.Sprintf("调试: %s", message))
				case "INFO":
					ua.appendLog(fmt.Sprintf("信息: %s", message))
				case "WARN":
					ua.appendLog(fmt.Sprintf("警告: %s", message))
				case "ERROR":
					ua.appendLog(fmt.Sprintf("错误: %s", message))
				default:
					ua.appendLog(fmt.Sprintf("未知: %s", message))
				}
			})

		}
		ua.tcpClient.OnConnected = func() {
			ua.playerManager.PlayersMutex.Lock()
			player := game.NewPlayer()
			ua.playerManager.Players = []*game.Player{
				player,
			}
			ua.playerManager.PlayersMutex.Unlock()
			ua.appendLog(fmt.Sprintf("信息：连接到服务器 %s", targetHost))
			err := ua.configManager.SetConnectionAddressAsLastUsed(targetHost)
			if err != nil {
				fyne.Do(func() {
					ua.appendLog(fmt.Sprintf("错误：保存连接地址失败: %v", err))
				})
			}
			ua.mainPage.SetClientStatus()
		}
		ua.tcpClient.OnDisconnected = func() {
			ua.playerManager.PlayersMutex.Lock()
			defer ua.playerManager.PlayersMutex.Unlock()
			ua.playerManager.Players = []*game.Player{}
			fyne.Do(func() {
				ua.mainPage.RefreshPlayerTable()
			})
		}
		ua.tcpClient.AddHandler(proto.ID_ServiceIDPackageID, func(m *easytcp.Message) {
			uploadBytesSize := new(int64)
			downloadBytesSize := new(int64)
			*uploadBytesSize = 0
			mData := m.Data()
			*downloadBytesSize = int64(len(mData) + 8)
			defer ua.UpdatePlayerTrafficByDeviceID("A", uploadBytesSize, downloadBytesSize)
			var respData proto.ServiceIDPackage
			if err := ua.tcpClient.Codec.Decode(mData, &respData); err != nil {
				fyne.Do(func() {
					ua.appendLog(fmt.Sprintf("错误：解析服务ID包失败: %v", err))
				})
				return
			}

			ua.serviceID = respData.ServiceId
			authCode, deviceID, err := ua.configManager.GetServerByServiceID(respData.ServiceId)
			authenticationID := ""
			if err != nil {
				fyne.Do(func() {
					ua.appendLog(fmt.Sprintf("信息：连接到新服务器：%s", respData.ServiceId))
				})
			} else {
				encryptedAuthCode, err := security.EncryptWithPublicKey(respData.PublicKey, []byte(authCode))
				if err != nil {
					fyne.Do(func() {
						ua.appendLog(fmt.Sprintf("错误：加密认证码失败: %v", err))
					})
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
			sendBytesSize, err := ua.tcpClient.Send(proto.ID_AuthenticationIDPackageID, req)
			if err != nil {
				fyne.Do(func() {
					ua.appendLog(fmt.Sprintf("错误：发送认证码失败: %v", err))
				})
				return
			}
			*uploadBytesSize = int64(sendBytesSize)
			ua.playerManager.PlayersMutex.Lock()
			defer ua.playerManager.PlayersMutex.Unlock()
			ua.playerManager.Players[0].DeviceID = deviceID
			remark, err := ua.configManager.GetDeviceServiceRemark(deviceID, ua.serviceID)
			if err == nil {
				ua.playerManager.Players[0].Remark = remark
			} else {
				ua.playerManager.Players[0].Remark = ""
			}
			fyne.Do(func() {
				ua.mainPage.RefreshPlayerTableItem(1, 0)
			})
		})
		ua.tcpClient.AddHandler(proto.ID_ConfirmRegisterPackageID, func(m *easytcp.Message) {
			uploadBytesSize := new(int64)
			downloadBytesSize := new(int64)
			*uploadBytesSize = 0
			mData := m.Data()
			*downloadBytesSize = int64(len(mData) + 8)
			defer ua.UpdatePlayerTrafficByDeviceID("A", uploadBytesSize, downloadBytesSize)
			var respData proto.ConfirmRegisterPackage
			if err := ua.tcpClient.Codec.Decode(mData, &respData); err != nil {
				fyne.Do(func() {
					ua.appendLog(fmt.Sprintf("错误：解析服务ID包失败: %v", err))
				})
				return
			}

			ua.configManager.InsertServer(ua.serviceID, respData.AuthenticationId, respData.DeviceId)
			remark, err := ua.configManager.GetDeviceServiceRemark(respData.DeviceId, ua.serviceID)
			ua.playerManager.PlayersMutex.Lock()
			ua.playerManager.Players[0].DeviceID = respData.DeviceId
			if err == nil {
				ua.playerManager.Players[0].Remark = remark
			} else {
				ua.playerManager.Players[0].Remark = ""
			}
			ua.playerManager.PlayersMutex.Unlock()
			fyne.Do(func() {
				ua.mainPage.RefreshPlayerTableItem(1, 0)
			})
		})
		ua.tcpClient.AddHandler(proto.ID_AllUserInfoPackageID, func(m *easytcp.Message) {
			uploadBytesSize := new(int64)
			downloadBytesSize := new(int64)
			*uploadBytesSize = 0
			mData := m.Data()
			*downloadBytesSize = int64(len(mData) + 8)
			defer ua.UpdatePlayerTrafficByDeviceID("A", uploadBytesSize, downloadBytesSize)
			var respData proto.AllUserInfoPackage
			if err := ua.tcpClient.Codec.Decode(mData, &respData); err != nil {
				fyne.Do(func() {
					ua.appendLog(fmt.Sprintf("错误：解析服务ID包失败: %v", err))
				})
				return
			}

			// 根据服务器发送的玩家数据更新本地玩家列表
			ua.playerManager.PlayersMutex.Lock()
			defer ua.playerManager.PlayersMutex.Unlock()

			// 获取本地玩家的设备ID
			localDeviceID := ""
			if len(ua.playerManager.Players) > 0 {
				localDeviceID = ua.playerManager.Players[0].DeviceID
			}

			// 创建设备ID到玩家数据的映射，便于查找
			serverPlayers := make(map[string]*proto.AllUserInfoPackage_UserData)
			for _, userData := range respData.UserDataList {
				// 跳过本地玩家，避免重复添加
				if userData.DeviceId == localDeviceID {
					ua.playerManager.Players[0].Port = int(userData.Port)
					if udpRelay, ok := ua.udpRelay[ua.playerManager.Players[0].Port]; ok {
						udpRelay.Stop()
						delete(ua.udpRelay, ua.playerManager.Players[0].Port)
					}
					continue
				}
				serverPlayers[userData.DeviceId] = userData
			}

			// 处理本地玩家列表：删除、修改、新增
			var updatedPlayers []*game.Player

			// 首先保留本地玩家（索引0）
			if len(ua.playerManager.Players) > 0 {
				localPlayer := ua.playerManager.Players[0]
				updatedPlayers = append(updatedPlayers, localPlayer)
			}

			// 处理服务器返回的玩家数据
			for deviceID, serverPlayer := range serverPlayers {
				// 查找是否已存在该玩家
				found := false
				for i := 1; i < len(ua.playerManager.Players); i++ { // 从1开始，跳过本地玩家
					if ua.playerManager.Players[i].DeviceID == deviceID {
						// 更新现有玩家信息
						ua.playerManager.Players[i].Port = int(serverPlayer.Port)
						ua.startUDPRelayOnClient(ua.playerManager.Players[i].Port)
						updatedPlayers = append(updatedPlayers, ua.playerManager.Players[i])
						found = true
						break
					}
				}

				if !found {
					player := game.NewPlayer()
					player.DeviceID = deviceID
					player.Port = int(serverPlayer.Port)

					// 尝试从数据库加载备注
					remark, err := ua.configManager.GetDeviceServiceRemark(deviceID, ua.serviceID)
					if err == nil {
						player.Remark = remark
					}
					if _, ok := ua.udpRelay[player.Port]; !ok {
						udpConfig := udp.DefaultConfig()
						udpConfig.Port = player.Port
						// 尝试启动UDP中继
						relay := udp.NewUDPService(udpConfig)
						relay.SetDataCallback(func(data []byte, addr *net.UDPAddr) {
							uploadBytesSize := new(int64)
							downloadBytesSize := new(int64)
							*uploadBytesSize = 0
							*downloadBytesSize = 0
							defer ua.UpdatePlayerTrafficByDeviceID("A", uploadBytesSize, downloadBytesSize)
							ua.playerManager.PlayersMutex.Lock()
							defer ua.playerManager.PlayersMutex.Unlock()
							for index, player := range ua.playerManager.Players {
								if index == 0 {
									if player.Port != addr.Port {
										player.Port = addr.Port
										req := &proto.BindPortPackage{
											Port: int32(player.Port),
										}
										sendBytesSize, err := ua.tcpClient.Send(proto.ID_BindPortPackageID, req)
										if err != nil {
											fyne.Do(func() {
												ua.appendLog(fmt.Sprintf("错误：发送绑定端口失败: %v", err))
											})
											return
										}
										*uploadBytesSize = *uploadBytesSize + int64(sendBytesSize)
									}
								} else if player.Port == udpConfig.Port {
									req := &proto.ForwardPackage{
										Port:  int32(udpConfig.Port),
										Bytes: data,
									}
									sendBytesSize, err := ua.tcpClient.Send(proto.ID_ForwardPackageID, req)
									if err != nil {
										fyne.Do(func() {
											ua.appendLog(fmt.Sprintf("转发数据给 %d 失败: %v", udpConfig.Port, err))
										})
										return
									}
									player.TotalUpload += int64(sendBytesSize)
									ua.playerManager.Players[0].TotalUpload += int64(sendBytesSize)
									break
								}
							}
						})
						err := relay.Start()
						if err != nil {
							fyne.Do(func() {
								ua.appendLog(fmt.Sprintf("错误：启动端口 %d 的UDP中继失败: %v", udpConfig.Port, err))
							})
						}
						ua.udpRelay[player.Port] = relay
					}

					updatedPlayers = append(updatedPlayers, player)
				}
			}

			// 更新玩家列表
			ua.playerManager.Players = updatedPlayers

			// 刷新表格显示
			fyne.Do(func() {
				ua.mainPage.RefreshPlayerTable()
			})
		})
		ua.tcpClient.AddHandler(proto.ID_ForwardPackageID, func(m *easytcp.Message) {
			ua.playerManager.PlayersMutex.Lock()
			deviceID := "A"
			port := 0
			targetPort := 0
			mData := m.Data()
			var respData proto.ForwardPackage
			if err := ua.tcpClient.Codec.Decode(mData, &respData); err != nil {
				fyne.Do(func() {
					ua.appendLog(fmt.Sprintf("错误：解析服务ID包失败: %v", err))
				})
				return
			}
			for index, player := range ua.playerManager.Players {
				if index == 0 {
					targetPort = player.Port
				} else if player.Port == int(respData.Port) {
					deviceID = player.DeviceID
					port = player.Port
					break
				}
			}
			ua.playerManager.PlayersMutex.Unlock()

			uploadBytesSize := new(int64)
			downloadBytesSize := new(int64)
			*uploadBytesSize = 0
			*downloadBytesSize = int64(len(mData) + 8)
			defer ua.UpdatePlayerTrafficByDeviceID(deviceID, uploadBytesSize, downloadBytesSize)

			if udpRelay, exists := ua.udpRelay[port]; exists {
				err := udpRelay.Send(respData.Bytes, "127.0.0.1:"+strconv.Itoa(targetPort))
				if err != nil {
					fyne.Do(func() {
						ua.appendLog(fmt.Sprintf("错误：发送UDP数据失败: %v", err))
					})
					return
				}
				return
			}
			fyne.Do(func() {
				ua.appendLog(fmt.Sprintf("错误：未找到端口 %d 的UDP中继", port))
			})
		})
		ua.tcpClient.AddHandler(proto.ID_AddOrUpdateDevicePackageID, func(m *easytcp.Message) {
			uploadBytesSize := new(int64)
			downloadBytesSize := new(int64)
			*uploadBytesSize = 0
			mData := m.Data()
			*downloadBytesSize = int64(len(mData) + 8)
			defer ua.UpdatePlayerTrafficByDeviceID("A", uploadBytesSize, downloadBytesSize)
			var respData proto.AddOrUpdateDevicePackage
			if err := ua.tcpClient.Codec.Decode(mData, &respData); err != nil {
				fyne.Do(func() {
					ua.appendLog(fmt.Sprintf("错误：解析服务ID包失败: %v", err))
				})
				return
			}

			ua.playerManager.PlayersMutex.Lock()
			defer ua.playerManager.PlayersMutex.Unlock()
			ua.startUDPRelayOnClient(int(respData.Port))
			for _, player := range ua.playerManager.Players {
				if player.DeviceID == respData.DeviceId {
					player.Port = int(respData.Port)
					return
				}
			}
			player := game.NewPlayer()
			player.DeviceID = respData.DeviceId
			player.Port = int(respData.Port)
			remark, err := ua.configManager.GetDeviceServiceRemark(respData.DeviceId, ua.serviceID)
			if err == nil {
				player.Remark = remark
			}
			ua.playerManager.Players = append(ua.playerManager.Players, player)
			fyne.Do(func() {
				ua.mainPage.RefreshPlayerTable()
			})
		})
		ua.tcpClient.AddHandler(proto.ID_RemoveDevicePackageID, func(m *easytcp.Message) {
			uploadBytesSize := new(int64)
			downloadBytesSize := new(int64)
			*uploadBytesSize = 0
			mData := m.Data()
			*downloadBytesSize = int64(len(mData) + 8)
			defer ua.UpdatePlayerTrafficByDeviceID("A", uploadBytesSize, downloadBytesSize)
			var respData proto.RemoveDevicePackage
			if err := ua.tcpClient.Codec.Decode(mData, &respData); err != nil {
				fyne.Do(func() {
					ua.appendLog(fmt.Sprintf("错误：解析服务ID包失败: %v", err))
				})
				return
			}

			ua.playerManager.PlayersMutex.Lock()
			defer ua.playerManager.PlayersMutex.Unlock()
			for index, player := range ua.playerManager.Players {
				if player.DeviceID == respData.DeviceId {
					ua.playerManager.Players = append(ua.playerManager.Players[:index], ua.playerManager.Players[index+1:]...)
				}
			}
		})
		err := ua.tcpClient.Connect(targetHost)
		if err != nil {
			ua.appendLog(fmt.Sprintf("错误：连接服务器失败: %v", err))
			return
		}
	}
	ua.mainPage.DisconnectBtn.OnTapped = func() {
		ua.tcpClient.Disconnect()
		ua.tcpClient = nil
		ua.playerManager.PlayersMutex.Lock()
		ua.playerManager.Players = make([]*game.Player, 0)
		ua.playerManager.PlayersMutex.Unlock()
		// 停止所有UDP中继服务器
		for port, relay := range ua.udpRelay {
			relay.Stop()
			delete(ua.udpRelay, port)
		}
		ua.mainPage.RefreshPlayerTable()
		ua.appendLog("信息：断开服务器连接")
		ua.mainPage.SetIdleStatus()
	}
	ua.mainPage.ClearBtn.OnTapped = func() {
		ua.mainPage.LogText.SetText("")
	}
	ua.mainPage.ClearBtn1.OnTapped = ua.mainPage.ClearBtn.OnTapped
	ua.mainPage.PlayerTable.CreateCell = func() fyne.CanvasObject {
		return widget.NewLabel("模板文本")
	}
	ua.mainPage.PlayerTable.Length = func() (int, int) {
		ua.playerManager.PlayersMutex.RLock()
		defer ua.playerManager.PlayersMutex.RUnlock()
		return len(ua.playerManager.Players) + 2, 5 // 行数(玩家数+表头+底部)，列数
	}
	ua.mainPage.PlayerTable.UpdateCell = func(id widget.TableCellID, template fyne.CanvasObject) {
		label := template.(*widget.Label)
		if id.Row == 0 {
			// 表头
			switch id.Col {
			case 0:
				label.SetText("玩家")
			case 1:
				label.SetText("端口")
			case 2:
				label.SetText("上传速度")
			case 3:
				label.SetText("下载速度")
			case 4:
				label.SetText("Ping")
			}
			return
		}

		ua.playerManager.PlayersMutex.RLock()
		defer ua.playerManager.PlayersMutex.RUnlock()

		if id.Row-1 < len(ua.playerManager.Players) {
			player := ua.playerManager.Players[id.Row-1]
			switch id.Col {
			case 0:
				// 展示备注，如果没有备注则展示ID
				if player.Remark != "" {
					label.SetText(player.Remark)
				} else {
					label.SetText(player.DeviceID)
				}
			case 1:
				label.SetText(strconv.Itoa(player.Port))
			case 2:
				label.SetText(player.UploadSpeed)
			case 3:
				label.SetText(player.DownloadSpeed)
			case 4:
				label.SetText(strconv.Itoa(player.Ping) + "ms")
			}
		} else {
			label.SetText("")
		}
	}
	ua.mainPage.Tabs.OnSelected = func(ti *container.TabItem) {
		// 保存当前选中的tab索引
		tabIndex := strconv.Itoa(ua.mainPage.Tabs.SelectedIndex())
		ua.configManager.SetConfigDebounced("last_tab", tabIndex)
	}
}

// 分配端口并启动UDP中继服务器
func (ua *UDPRelayApp) allocatePortAndStartRelay(deviceID string) (int, error) {
	// 检查是否已为该设备分配端口
	if port, exists := ua.reservePort[deviceID]; exists {
		// 检查UDP中继是否还在运行
		if relay, ok := ua.udpRelay[port]; ok && relay.IsRunning() {
			return port, nil
		}
		// 如果中继已停止，删除记录重新分配
		delete(ua.reservePort, deviceID)
		delete(ua.udpRelay, port)
	}

	udpConfig := udp.DefaultConfig()
	// 查找可用端口（从游戏端口+1开始）
	startPort := ua.targetPort + 1
	for port := startPort; port < startPort+100; port++ {
		if _, exists := ua.udpRelay[port]; !exists {
			udpConfig.Port = port
			// 尝试启动UDP中继
			relay := udp.NewUDPService(udpConfig)
			err := relay.Start()
			if err != nil {
				// 端口可能被占用，尝试下一个
				continue
			}

			localPort := port
			relay.SetDataCallback(func(data []byte, addr *net.UDPAddr) {
				ua.playerManager.PlayersMutex.Lock()
				defer ua.playerManager.PlayersMutex.Unlock()
				for index, player := range ua.playerManager.Players {
					if index == 0 {
						if player.Port != addr.Port {

						}
					} else if player.Port == localPort {
						req := &proto.ForwardPackage{
							Port:  int32(addr.Port),
							Bytes: data,
						}
						sendBytesSize, err := ua.tcpService.SendToSession(player.SessionID, proto.ID_ForwardPackageID, req)
						if err != nil {
							fyne.Do(func() {
								ua.appendLog(fmt.Sprintf("转发数据给 %s 失败: %v", player.DeviceID, err))
							})
							return
						}
						player.TotalUpload += int64(sendBytesSize)
						ua.playerManager.Players[0].TotalUpload += int64(sendBytesSize)
						break
					}
				}
			})

			// 记录分配的端口和UDP中继实例
			ua.reservePort[deviceID] = port
			ua.udpRelay[port] = relay

			fyne.Do(func() {
				ua.appendLog(fmt.Sprintf("为设备 %s 分配端口 %d，启动UDP中继", deviceID, port))
			})
			return port, nil
		}
	}

	return 0, errors.New("无法找到可用端口")
}

// 根据指定端口在TCP客户端侧启动UDP中继
func (ua *UDPRelayApp) startUDPRelayOnClient(port int) error {
	// 检查是否已存在该端口的UDP中继
	if _, exists := ua.udpRelay[port]; exists {
		return nil // 已存在，无需重复启动
	}

	udpConfig := udp.DefaultConfig()
	udpConfig.Port = port

	// 创建UDP中继服务
	relay := udp.NewUDPService(udpConfig)

	// 设置数据回调函数
	relay.SetDataCallback(func(data []byte, addr *net.UDPAddr) {
		uploadBytesSize := new(int64)
		downloadBytesSize := new(int64)
		*uploadBytesSize = 0
		*downloadBytesSize = 0
		defer ua.UpdatePlayerTrafficByDeviceID("A", uploadBytesSize, downloadBytesSize)

		ua.playerManager.PlayersMutex.Lock()
		defer ua.playerManager.PlayersMutex.Unlock()

		// 找到对应端口的玩家并转发数据
		for index, player := range ua.playerManager.Players {
			if index == 0 {
				if player.Port != addr.Port {
					player.Port = addr.Port
					req := &proto.BindPortPackage{
						Port: int32(player.Port),
					}
					sendBytesSize, err := ua.tcpClient.Send(proto.ID_BindPortPackageID, req)
					if err != nil {
						fyne.Do(func() {
							ua.appendLog(fmt.Sprintf("错误：发送绑定端口失败: %v", err))
						})
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
				sendBytesSize, err := ua.tcpClient.Send(proto.ID_ForwardPackageID, req)
				if err != nil {
					fyne.Do(func() {
						ua.appendLog(fmt.Sprintf("转发数据给端口 %d 失败: %v", udpConfig.Port, err))
					})
					return
				}

				// 更新流量统计
				player.TotalUpload += int64(sendBytesSize)
				ua.playerManager.Players[0].TotalUpload += int64(sendBytesSize)
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
	ua.udpRelay[port] = relay

	fyne.Do(func() {
		ua.appendLog(fmt.Sprintf("信息：在端口 %d 启动UDP中继", port))
	})

	return nil
}

// 添加日志到界面
func (ua *UDPRelayApp) appendLog(message string) {
	currentText := ua.mainPage.LogText.Text
	if currentText != "" {
		currentText += "\n"
	}
	currentText += message
	ua.mainPage.LogText.SetText(currentText)
	ua.mainPage.LogText.CursorRow = strings.Count(currentText, "\n")
}

// Run 运行应用程序
func (ua *UDPRelayApp) Run() {
	ua.initData()
	ua.setEventCallbacks()

	ua.mainPage.PlayerTable.SetColumnWidth(0, 150) // 玩家列宽度
	ua.mainPage.PlayerTable.SetColumnWidth(1, 80)  // 端口列宽度
	ua.mainPage.PlayerTable.SetColumnWidth(2, 100) // 上传速度列宽度
	ua.mainPage.PlayerTable.SetColumnWidth(3, 100) // 下载速度列宽度
	ua.mainPage.PlayerTable.SetColumnWidth(4, 80)  // Ping列宽度
	
	// 恢复窗口状态
	ua.restoreWindowState()

	// 设置窗口关闭事件处理
	ua.mainPage.Window.SetCloseIntercept(func() {
		// 保存窗口状态
		ua.saveWindowState()
		// 关闭窗口
		ua.mainPage.Window.Close()
	})

	ua.mainPage.ShowAndRun()
}

// 启动速度计算定时器
func (ua *UDPRelayApp) StartSpeedTimer() {
	ticker := time.NewTicker(1 * time.Second)
	go func() {
		for range ticker.C {
			ua.calculateAndUpdateSpeeds()
		}
	}()
}

// 计算并更新所有玩家的速度
func (ua *UDPRelayApp) calculateAndUpdateSpeeds() {
	ua.playerManager.PlayersMutex.Lock()
	defer ua.playerManager.PlayersMutex.Unlock()

	for row, player := range ua.playerManager.Players {
		// 计算上传速度
		uploadDiff := player.TotalUpload - player.LastUpload
		player.LastUpload = player.TotalUpload
		uploadSpeedStr := formatSpeed(uploadDiff)
		if player.UploadSpeed != uploadSpeedStr {
			player.UploadSpeed = uploadSpeedStr
			// 更新表格显示
			fyne.Do(func() {
				ua.mainPage.RefreshPlayerTableItem(row+1, 2) // +1是因为第0行是表头
			})
		}

		// 计算下载速度
		downloadDiff := player.TotalDownload - player.LastDownload
		player.LastDownload = player.TotalDownload
		downloadSpeedStr := formatSpeed(downloadDiff)
		if player.DownloadSpeed != downloadSpeedStr {
			player.DownloadSpeed = downloadSpeedStr
			// 更新表格显示
			fyne.Do(func() {
				ua.mainPage.RefreshPlayerTableItem(row+1, 3) // +1是因为第0行是表头
			})
		}
	}
}

// formatSpeed 格式化速度显示，保留两位小数并自动匹配单位
func formatSpeed(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%.2f B/s", float64(bytes))
	} else if bytes < 1024*1024 {
		return fmt.Sprintf("%.2f KB/s", float64(bytes)/1024)
	} else if bytes < 1024*1024*1024 {
		return fmt.Sprintf("%.2f MB/s", float64(bytes)/(1024*1024))
	} else {
		return fmt.Sprintf("%.2f GB/s", float64(bytes)/(1024*1024*1024))
	}
}

// 根据sessionID更新玩家流量统计
func (ua *UDPRelayApp) UpdatePlayerTrafficBySessionID(sessionID string, uploadBytes *int64, downloadBytes *int64) {
	if *uploadBytes == 0 && *downloadBytes == 0 {
		return
	}

	ua.playerManager.PlayersMutex.Lock()
	defer ua.playerManager.PlayersMutex.Unlock()

	for index, player := range ua.playerManager.Players {
		if index == 0 || player.SessionID == sessionID {
			player.TotalUpload += *uploadBytes
			player.TotalDownload += *downloadBytes
		}
	}
}

// 根据DeviceID更新玩家流量统计
func (ua *UDPRelayApp) UpdatePlayerTrafficByDeviceID(deviceID string, uploadBytes *int64, downloadBytes *int64) {
	if *uploadBytes == 0 && *downloadBytes == 0 {
		return
	}

	ua.playerManager.PlayersMutex.Lock()
	defer ua.playerManager.PlayersMutex.Unlock()

	for index, player := range ua.playerManager.Players {
		if index == 0 || player.DeviceID == deviceID {
			player.TotalUpload += *uploadBytes
			player.TotalDownload += *downloadBytes
		}
	}
}

// 保存窗口大小
func (ua *UDPRelayApp) saveWindowState() {
	// 保存窗口大小
	size := ua.mainPage.Window.Content().Size()
	ua.configManager.SetConfigDebounced("window_width", strconv.Itoa(int(size.Width)))
	ua.configManager.SetConfigDebounced("window_height", strconv.Itoa(int(size.Height)))
}

// 恢复窗口大小
func (ua *UDPRelayApp) restoreWindowState() {
	// 恢复窗口大小
	width, _ := strconv.Atoi(ua.configManager.GetConfig("window_width", "1024"))
	height, _ := strconv.Atoi(ua.configManager.GetConfig("window_height", "768"))
	ua.mainPage.Window.Resize(fyne.NewSize(float32(width), float32(height)))
}

// 在应用程序退出时关闭配置管理器
func (ua *UDPRelayApp) Cleanup() {
	ua.configManager.Close()
}

func main() {
	relayApp, err := NewUDPRelayApp()
	if err != nil {
		myApp := app.New()
		window := myApp.NewWindow("UDP 中继服务器")
		window.Resize(fyne.Size{Width: 600, Height: 300})
		window.CenterOnScreen()

		// 创建错误标签
		errorLabel := widget.NewLabel(err.Error())
		errorLabel.Wrapping = fyne.TextWrapWord // 允许文本换行

		// 创建确定按钮
		confirmBtn := widget.NewButton("确定", func() {
			window.Close()
		})

		// 创建主要内容区域
		mainContent := container.NewVBox(
			widget.NewLabelWithStyle("错误", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
			errorLabel,
		)

		// 使用 Border 布局，将按钮放在底部
		content := container.NewBorder(
			nil,         // 上
			confirmBtn,  // 下（按钮在底部）
			nil,         // 左
			nil,         // 右
			mainContent, // 中间内容
		)

		// 添加外边距
		paddedContent := container.NewPadded(content)

		window.SetContent(paddedContent)
		window.ShowAndRun()
		return
	}
	relayApp.StartSpeedTimer()
	relayApp.Run()
	relayApp.configManager.WaitForDebounced()
	// 清理资源
	relayApp.Cleanup()
}
