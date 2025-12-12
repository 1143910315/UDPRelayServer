package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"image/color"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/1143910315/UDPRelayServer/net/packer"
	"github.com/1143910315/UDPRelayServer/net/tcp"
	"github.com/1143910315/UDPRelayServer/net/udp"
	"github.com/1143910315/UDPRelayServer/utils"
	"github.com/DarthPestilane/easytcp"
	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
)

// 自定义主题结构体
type CustomTheme struct {
}

func (c CustomTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	//log.Println("name:" + string(name) + ", variant:" + strconv.FormatUint(uint64(variant), 10) + ", color:" + fmt.Sprintf("%#v", theme.DefaultTheme().Color(name, variant)))
	if name == theme.ColorNameDisabled {
		if variant == theme.VariantLight {
			return color.RGBA{R: 128, G: 128, B: 128, A: 255}
		} else {
			return color.RGBA{R: 128, G: 128, B: 128, A: 255}
		}
	}
	return theme.DefaultTheme().Color(name, variant)
}

func (c CustomTheme) Font(style fyne.TextStyle) fyne.Resource {
	return theme.DefaultTheme().Font(style)
}

func (c CustomTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return theme.DefaultTheme().Icon(name)
}

func (c CustomTheme) Size(name fyne.ThemeSizeName) float32 {
	return theme.DefaultTheme().Size(name)
}

// 玩家结构体
type Player struct {
	SessionID     string
	DeviceID      string
	Remark        string
	Port          int
	TotalUpload   int64
	TotalDownload int64
	LastUpload    int64
	LastDownload  int64
	DownloadSpeed string
	UploadSpeed   string
	Ping          int
}

// 配置管理结构体
type ConfigManager struct {
	db             *sql.DB
	mutex          sync.RWMutex
	debounceTimers map[string]*time.Timer
	debounceWg     sync.WaitGroup
}

// 初始化配置管理器
func NewConfigManager() (*ConfigManager, error) {
	db, err := sql.Open("sqlite3", "./udp_relay_config.db")
	if err != nil {
		return nil, err
	}

	// 创建配置表
	createTableSQL := "CREATE TABLE IF NOT EXISTS app_config (key TEXT PRIMARY KEY, value TEXT NOT NULL)"
	_, err = db.Exec(createTableSQL)
	if err != nil {
		return nil, err
	}

	// 创建密钥表
	createKeyTableSQL := "CREATE TABLE IF NOT EXISTS key_pairs (id INTEGER PRIMARY KEY,	service_id TEXT NOT NULL UNIQUE, private_key TEXT NOT NULL, public_key TEXT NOT NULL)"
	_, err = db.Exec(createKeyTableSQL)
	if err != nil {
		return nil, err
	}

	// 创建服务器表
	createServerTableSQL := "CREATE TABLE IF NOT EXISTS servers (service_id TEXT PRIMARY KEY, auth_code TEXT NOT NULL, device_id TEXT NOT NULL)"
	_, err = db.Exec(createServerTableSQL)
	if err != nil {
		return nil, err
	}

	// 创建设备认证表
	createDeviceAuthTableSQL := "CREATE TABLE IF NOT EXISTS device_authentication (device_id TEXT PRIMARY KEY, authentication_id TEXT NOT NULL)"
	_, err = db.Exec(createDeviceAuthTableSQL)
	if err != nil {
		return nil, err
	}

	// 创建设备服务备注表
	createDeviceServiceRemarkTableSQL := "CREATE TABLE IF NOT EXISTS device_service_remark (device_id TEXT, service_id TEXT, remark TEXT, PRIMARY KEY (device_id, service_id))"
	_, err = db.Exec(createDeviceServiceRemarkTableSQL)
	if err != nil {
		return nil, err
	}

	return &ConfigManager{
		db:             db,
		debounceTimers: make(map[string]*time.Timer),
	}, nil
}

// 保存配置项
func (cm *ConfigManager) SetConfig(key, value string) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	_, err := cm.db.Exec("INSERT OR REPLACE INTO app_config (key, value) VALUES (?, ?)", key, value)
	return err
}

// 带防抖的配置管理器方法
func (cm *ConfigManager) SetConfigDebounced(key, value string) {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	// 如果已有定时器，先停止
	if timer, exists := cm.debounceTimers[key]; exists {
		if timer.Stop() {
			// 如果成功停止了计时器，减少WaitGroup计数
			cm.debounceWg.Done()
		}
	}

	// 增加WaitGroup计数
	cm.debounceWg.Add(1)

	// 创建新的定时器
	timer := time.AfterFunc(2000*time.Millisecond, func() {
		cm.mutex.Lock()
		delete(cm.debounceTimers, key)
		cm.mutex.Unlock()
		cm.SetConfig(key, value)
		// 执行完成后减少计数
		cm.debounceWg.Done()
	})

	// 存储定时器引用
	cm.debounceTimers[key] = timer
}

// 读取配置项
func (cm *ConfigManager) GetConfig(key, defaultValue string) string {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	var value string
	err := cm.db.QueryRow("SELECT value FROM app_config WHERE key = ?", key).Scan(&value)
	if err != nil {
		return defaultValue
	}
	return value
}

// 生成密钥对并插入数据库，最多尝试三次
func (cm *ConfigManager) insertKeyPair(id int) (string, string, string, error) {
	for range 3 {
		publicKey, privateKey, err := GenerateKeyPair()
		if err != nil {
			return "", "", "", err
		}

		serviceId, err := uuid.NewUUID()
		if err != nil {
			return "", "", "", err
		}

		_, err = cm.db.Exec("INSERT INTO key_pairs (id, service_id, private_key, public_key) VALUES (?, ?, ?, ?)", id, serviceId.String(), privateKey, publicKey)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint") {
				// service_id 重复，继续重试
				continue
			}
			return "", "", "", err
		}

		return serviceId.String(), privateKey, publicKey, nil
	}
	return "", "", "", errors.New("插入密钥对失败，已达到最大重试次数")
}

// 从数据库获取密钥对
func (cm *ConfigManager) GetKeyPairByID(id int) (string, string, string, error) {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	var serviceId, privateKey, publicKey string
	err := cm.db.QueryRow("SELECT service_id, private_key, public_key FROM key_pairs WHERE id = ?", id).
		Scan(&serviceId, &privateKey, &publicKey)
	if err != nil {
		if err == sql.ErrNoRows {
			// 数据不存在，插入新数据
			return cm.insertKeyPair(id)
		}
		return "", "", "", err
	}
	return serviceId, privateKey, publicKey, nil
}

// 根据serviceId查询认证码和设备id
func (cm *ConfigManager) GetServerByServiceId(serviceId string) (string, string, error) {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	var authCode, deviceId string
	err := cm.db.QueryRow("SELECT auth_code, device_id FROM servers WHERE service_id = ?", serviceId).
		Scan(&authCode, &deviceId)
	if err != nil {
		return "", "", err
	}
	return authCode, deviceId, nil
}

// 插入serviceId、认证码、设备id
func (cm *ConfigManager) InsertServer(serviceId, authCode, deviceId string) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	_, err := cm.db.Exec("INSERT INTO servers (service_id, auth_code, device_id) VALUES (?, ?, ?)", serviceId, authCode, deviceId)
	return err
}

// 设置设备认证信息
func (cm *ConfigManager) SetDeviceAuthentication(deviceId, authenticationId string) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	_, err := cm.db.Exec("INSERT OR REPLACE INTO device_authentication (device_id, authentication_id) VALUES (?, ?)", deviceId, authenticationId)
	return err
}

// 获取设备认证信息
func (cm *ConfigManager) GetDeviceAuthentication(deviceId string) (string, error) {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	var authenticationId string
	err := cm.db.QueryRow("SELECT authentication_id FROM device_authentication WHERE device_id = ?", deviceId).Scan(&authenticationId)
	if err != nil {
		return "", err
	}
	return authenticationId, nil
}

// 设置设备服务备注
func (cm *ConfigManager) SetDeviceServiceRemark(deviceId, serviceId, remark string) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	_, err := cm.db.Exec("INSERT OR REPLACE INTO device_service_remark (device_id, service_id, remark) VALUES (?, ?, ?)", deviceId, serviceId, remark)
	return err
}

// 获取设备服务备注
func (cm *ConfigManager) GetDeviceServiceRemark(deviceId, serviceId string) (string, error) {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	var remark string
	err := cm.db.QueryRow("SELECT remark FROM device_service_remark WHERE device_id = ? AND service_id = ?", deviceId, serviceId).Scan(&remark)
	if err != nil {
		return "", err
	}
	return remark, nil
}

// 关闭数据库连接
func (cm *ConfigManager) Close() error {
	return cm.db.Close()
}

// 使用WaitGroup等待
func (cm *ConfigManager) WaitForDebounced() {
	// 等待所有正在执行的防抖操作完成
	cm.debounceWg.Wait()
}

// 生成RSA密钥对
func GenerateKeyPair() (string, string, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", err
	}

	// 编码私钥为PEM格式
	privateKeyBytes := x509.MarshalPKCS1PrivateKey(privateKey)
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: privateKeyBytes,
	})

	// 编码公钥为PEM格式
	publicKeyBytes, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		return "", "", err
	}
	publicKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PUBLIC KEY",
		Bytes: publicKeyBytes,
	})

	return string(publicKeyPEM), string(privateKeyPEM), nil
}

// 使用公钥加密数据
func EncryptWithPublicKey(publicKeyPEM string, data []byte) ([]byte, error) {
	block, _ := pem.Decode([]byte(publicKeyPEM))
	if block == nil {
		return nil, errors.New("failed to parse PEM block containing the public key")
	}

	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}

	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("not an RSA public key")
	}

	// 使用OAEP加密
	encrypted, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, rsaPub, data, nil)
	if err != nil {
		return nil, err
	}

	return encrypted, nil
}

// 使用私钥解密数据
func DecryptWithPrivateKey(privateKeyPEM string, encrypted []byte) ([]byte, error) {
	block, _ := pem.Decode([]byte(privateKeyPEM))
	if block == nil {
		return nil, errors.New("failed to parse PEM block containing the private key")
	}

	priv, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}

	// 使用OAEP解密
	decrypted, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, priv, encrypted, nil)
	if err != nil {
		return nil, err
	}

	return decrypted, nil
}

// GUI应用程序结构
type UDPRelayApp struct {
	app                       fyne.App
	window                    fyne.Window
	tcpService                *tcp.TCPServer
	tcpClient                 *tcp.TCPClient
	logText                   *widget.Entry
	startBtn                  *widget.Button
	stopBtn                   *widget.Button
	connectBtn                *widget.Button
	disconnectBtn             *widget.Button
	configManager             *ConfigManager
	playerTable               *widget.Table
	players                   []*Player
	playersMutex              sync.RWMutex
	advancedStringIncrementer *utils.AdvancedStringIncrementer
	serviceId                 string
	listenPort                int
	reservePort               map[string]int
	udpRelay                  map[int]*udp.UDPService
}

// NewUDPRelayApp 创建新的GUI应用程序
func NewUDPRelayApp() *UDPRelayApp {
	myApp := app.New()
	window := myApp.NewWindow("UDP 中继服务器")

	// 初始化配置管理器
	configManager, err := NewConfigManager()
	if err != nil {
		// 如果配置管理器初始化失败，使用默认值继续运行
		fmt.Printf("配置管理器初始化失败: %v\n", err)
	}

	// 设置自定义主题
	myApp.Settings().SetTheme(&CustomTheme{})

	return &UDPRelayApp{
		app:                       myApp,
		window:                    window,
		configManager:             configManager,
		advancedStringIncrementer: utils.NewAdvancedStringIncrementer(),
		reservePort:               make(map[string]int),
		udpRelay:                  make(map[int]*udp.UDPService),
	}
}

// createUI 创建用户界面
func (ua *UDPRelayApp) createUI() {
	// 创建输入字段
	listenAddressEntry := widget.NewEntry()
	targetHostEntry := widget.NewEntry()
	targetPortEntry := widget.NewEntry()

	// 从数据库加载配置，如果失败则使用默认值
	if ua.configManager != nil {
		listenAddressEntry.SetText(ua.configManager.GetConfig("listen_port", "8080"))
		targetHostEntry.SetText(ua.configManager.GetConfig("target_host", "127.0.0.1:8080"))
		targetPortEntry.SetText(ua.configManager.GetConfig("target_port", "8081"))

		// 设置配置保存回调
		listenAddressEntry.OnChanged = func(text string) {
			ua.configManager.SetConfigDebounced("listen_port", text)
		}
		targetHostEntry.OnChanged = func(text string) {
			ua.configManager.SetConfigDebounced("target_host", text)
		}
		targetPortEntry.OnChanged = func(text string) {
			ua.configManager.SetConfigDebounced("target_port", text)
		}
	} else {
		listenAddressEntry.SetText("8080")
		targetHostEntry.SetText("127.0.0.1:8080")
		targetPortEntry.SetText("8081")
	}

	listenAddressEntry.SetPlaceHolder("监听端口")
	targetHostEntry.SetPlaceHolder("目标主机")
	targetPortEntry.SetPlaceHolder("游戏端口")

	// 主机模式按钮
	ua.startBtn = widget.NewButton("启动服务器", func() {
		listenAddress := strings.TrimSpace(listenAddressEntry.Text)
		listenPort, err := strconv.Atoi(listenAddress)
		if err != nil || listenPort < 1 || listenPort > 65535 {
			ua.appendLog("错误: 监听端口必须是 1-65535 之间的数字")
			return
		}
		ua.listenPort = listenPort

		ua.tcpService = tcp.NewTCPServer()
		ua.tcpService.OnClientConnected = func(sessionID string) {
			uploadBytesSize := new(int64)
			downloadBytesSize := new(int64)
			*uploadBytesSize = 0
			*downloadBytesSize = 0
			defer ua.UpdatePlayerTrafficBySessionID(sessionID, uploadBytesSize, downloadBytesSize)

			ua.playersMutex.Lock()
			defer ua.playersMutex.Unlock()
			player := &Player{
				SessionID:     sessionID,
				DeviceID:      "",
				Remark:        "", // 初始备注为空
				Port:          0,  // 初始端口为0，后续可更新
				TotalUpload:   0,
				TotalDownload: 0,
				LastUpload:    0,
				LastDownload:  0,
				UploadSpeed:   "0.00 B/s",
				DownloadSpeed: "0.00 B/s",
				Ping:          0,
			}
			ua.players = append(ua.players, player)
			fyne.Do(func() {
				ua.appendLog(fmt.Sprintf("客户端连接: %s", sessionID))
				ua.refreshPlayerTable() // 刷新表格显示
			})
			serviceId, _, publicKey, err := ua.configManager.GetKeyPairByID(0)
			if err != nil {
				fyne.Do(func() {
					ua.appendLog(fmt.Sprintf("获取密钥对失败: %v", err))
				})
				return
			}
			req := &packer.ServiceIdPackage{
				Index:     0,
				ServiceId: serviceId,
				PublicKey: publicKey,
			}
			sendBytesSize, err := ua.tcpService.SendToSession(sessionID, packer.ID_ServiceIdPackageID, req)
			if err != nil {
				fyne.Do(func() {
					ua.appendLog(fmt.Sprintf("发送密钥对失败: %v", err))
				})
				return
			}
			*uploadBytesSize = int64(sendBytesSize)
		}
		ua.tcpService.OnClientDisconnected = func(sessionID string) {
			ua.playersMutex.Lock()
			defer ua.playersMutex.Unlock()

			for i, player := range ua.players {
				if player.SessionID == sessionID {
					ua.players = append(ua.players[:i], ua.players[i+1:]...)
					break
				}
			}
			fyne.Do(func() {
				ua.appendLog(fmt.Sprintf("客户端断开: %s", sessionID))
				ua.refreshPlayerTable() // 刷新表格显示
			})
		}
		ua.tcpService.AddRoute(packer.ID_AuthenticationIdPackageID, func(ctx easytcp.Context) {
			uploadBytesSize := new(int64)
			downloadBytesSize := new(int64)
			*uploadBytesSize = 0
			var reqData packer.AuthenticationIdPackage
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
				nextDeviceId := ua.advancedStringIncrementer.IncrementString(ua.configManager.GetConfig("max_generated_device_id", "A"))
				ua.configManager.SetConfig("max_generated_device_id", nextDeviceId)
				uuidObj, _ := uuid.NewUUID()
				authenticationId := uuidObj.String()
				ua.configManager.SetDeviceAuthentication(nextDeviceId, authenticationId)
				req := &packer.ConfirmRegisterPackage{
					AuthenticationId: authenticationId,
					DeviceId:         nextDeviceId,
				}
				sendBytesSize, err := ua.tcpService.SendToSession(sessionID, packer.ID_ConfirmRegisterPackageID, req)
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
				ua.playersMutex.Lock()
				defer ua.playersMutex.Unlock()
				req1 := &packer.AllUserInfoPackage{
					UserDataList: []*packer.AllUserInfoPackage_UserData{},
				}
				for index, player := range ua.players {
					if player.SessionID == sessionID {
						player.DeviceID = nextDeviceId
						player.Port = port
						remark, err := ua.configManager.GetDeviceServiceRemark(nextDeviceId, "")
						if err == nil {
							player.Remark = remark
						} else {
							player.Remark = ""
						}
						fyne.Do(func() {
							ua.refreshPlayerTableItem(index+1, 0)
							ua.refreshPlayerTableItem(index+1, 1)
						})
						break
					}
					if player.DeviceID != "" {
						req1.UserDataList = append(req1.UserDataList, &packer.AllUserInfoPackage_UserData{
							DeviceId: player.DeviceID,
							Port:     int32(player.Port),
						})
					}
				}
				packedMsg, err := ua.tcpService.PackerData(packer.ID_AllUserInfoPackageID, req)
				if err != nil {
					fyne.Do(func() {
						ua.appendLog(fmt.Sprintf("打包用户信息包失败: %v", err))
					})
					return
				}
				sendBytesSize = len(packedMsg)
				for _, player := range ua.players {
					if player.DeviceID != "" {
						err := ua.tcpService.SendRawToSession(player.SessionID, packedMsg)
						if err != nil {
							fyne.Do(func() {
								ua.appendLog(fmt.Sprintf("发送用户信息包失败: %v", err))
							})
							continue
						}
						player.TotalUpload += int64(sendBytesSize)
					}
				}
			} else {
				_, privateKey, _, err := ua.configManager.GetKeyPairByID(int(reqData.Index))
				if err == nil {
					encryptedData, err := base64.StdEncoding.DecodeString(reqData.AuthenticationId)
					if err == nil {
						authenticationId, err := DecryptWithPrivateKey(privateKey, encryptedData)
						if err == nil {
							authentication, err := ua.configManager.GetDeviceAuthentication(reqData.DeviceId)
							if err == nil && string(authenticationId) == authentication {
								// 为设备分配端口并启动UDP中继
								port, err := ua.allocatePortAndStartRelay(reqData.DeviceId)
								if err != nil {
									fyne.Do(func() {
										ua.appendLog(fmt.Sprintf("为设备 %s 分配端口失败: %v", reqData.DeviceId, err))
									})
									return
								}
								ua.playersMutex.Lock()
								defer ua.playersMutex.Unlock()
								req := &packer.AllUserInfoPackage{
									UserDataList: []*packer.AllUserInfoPackage_UserData{},
								}
								for index, player := range ua.players {
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
											ua.refreshPlayerTableItem(index+1, 0)
											ua.refreshPlayerTableItem(index+1, 1)
										})
									}
									if player.DeviceID != "" {
										req.UserDataList = append(req.UserDataList, &packer.AllUserInfoPackage_UserData{
											DeviceId: player.DeviceID,
											Port:     int32(player.Port),
										})
									}
								}
								packedMsg, err := ua.tcpService.PackerData(packer.ID_AllUserInfoPackageID, req)
								if err != nil {
									fyne.Do(func() {
										ua.appendLog(fmt.Sprintf("打包用户信息包失败: %v", err))
									})
									return
								}
								sendBytesSize := len(packedMsg)
								for _, player := range ua.players {
									if player.DeviceID != "" {
										err := ua.tcpService.SendRawToSession(player.SessionID, packedMsg)
										if err != nil {
											fyne.Do(func() {
												ua.appendLog(fmt.Sprintf("发送用户信息包失败: %v", err))
											})
											continue
										}
										player.TotalUpload += int64(sendBytesSize)
									}
								}
								return
							}
						}
					}
				}
				serviceId, _, publicKey, err := ua.configManager.GetKeyPairByID(int(reqData.Index) + 1)
				if err != nil {
					fyne.Do(func() {
						ua.appendLog(fmt.Sprintf("获取密钥对失败: %v", err))
					})
					return
				}
				req := &packer.ServiceIdPackage{
					Index:     reqData.Index + 1,
					ServiceId: serviceId,
					PublicKey: publicKey,
				}
				sendBytesSize, err := ua.tcpService.SendToSession(sessionID, packer.ID_ServiceIdPackageID, req)
				if err != nil {
					fyne.Do(func() {
						ua.appendLog(fmt.Sprintf("发送密钥对失败: %v", err))
					})
					return
				}
				*uploadBytesSize = int64(sendBytesSize)
			}
		})
		ua.tcpService.AddRoute(packer.ID_ForwardPackageID, func(ctx easytcp.Context) {
			uploadBytesSize := new(int64)
			downloadBytesSize := new(int64)
			*uploadBytesSize = 0
			var reqData packer.ForwardPackage
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
			ua.playersMutex.Lock()
			defer ua.playersMutex.Unlock()
			port := 0
			var sendPlayer *Player
			receiveSessionID := ""
			for index, player := range ua.players {
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
							req := &packer.ForwardPackage{
								Port:  int32(sendPlayer.Port),
								Bytes: reqData.Bytes,
							}
							sendBytesSize, err := ua.tcpService.SendToSession(receiveSessionID, packer.ID_ForwardPackageID, req)
							if err != nil {
								fyne.Do(func() {
									ua.appendLog(fmt.Sprintf("发送转发包失败: %v", err))
								})
								return
							}
							player.TotalUpload += int64(sendBytesSize)
							ua.players[0].TotalUpload += int64(sendBytesSize)
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
		ua.tcpService.AddRoute(packer.ID_BindPortPackageID, func(ctx easytcp.Context) {
			uploadBytesSize := new(int64)
			downloadBytesSize := new(int64)
			*uploadBytesSize = 0
			var reqData packer.ForwardPackage
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

			ua.playersMutex.Lock()
			defer ua.playersMutex.Unlock()
			for index, player := range ua.players {
				if player.SessionID == sessionID {
					player.Port = int(reqData.Port)
					ua.reservePort[player.DeviceID] = player.Port
					if _, ok := ua.udpRelay[player.Port]; !ok {
						udpConfig := udp.DefaultConfig()
						udpConfig.Port = player.Port
						relay := udp.NewUDPService(udpConfig)
						relay.SetDataCallback(func(data []byte, addr *net.UDPAddr) {
							ua.playersMutex.Lock()
							defer ua.playersMutex.Unlock()
							for index, player := range ua.players {
								if index == 0 {
									if player.Port != addr.Port {

									}
								} else if player.Port == udpConfig.Port {
									req := &packer.ForwardPackage{
										Port:  int32(ua.players[0].Port),
										Bytes: reqData.Bytes,
									}
									sendBytesSize, err := ua.tcpService.SendToSession(player.SessionID, packer.ID_ForwardPackageID, req)
									if err != nil {
										fyne.Do(func() {
											ua.appendLog(fmt.Sprintf("发送转发包失败: %v", err))
										})
										return
									}
									player.TotalUpload += int64(sendBytesSize)
									ua.players[0].TotalUpload += int64(sendBytesSize)
									break
								}
							}
						})
						relay.Start()
						ua.udpRelay[player.Port] = relay
					}
					fyne.Do(func() {
						ua.refreshPlayerTableItem(index+1, 1)
					})
				}
			}
		})
		err = ua.tcpService.Start(":" + listenAddress)
		if err != nil {
			ua.appendLog(fmt.Sprintf("启动TCP服务器失败: %v", err))
			return
		}
		ua.playersMutex.Lock()
		ua.players = []*Player{
			{
				SessionID:     "",
				DeviceID:      "A",
				Remark:        "",
				Port:          8080,
				TotalUpload:   0,
				TotalDownload: 0,
				LastUpload:    0,
				LastDownload:  0,
				UploadSpeed:   "0.00 B/s",
				DownloadSpeed: "0.00 B/s",
				Ping:          0,
			},
		}
		ua.playersMutex.Unlock()
		ua.refreshPlayerTable()

		ua.appendLog(fmt.Sprintf("信息：TCP服务器启动成功，监听 %s", listenAddress))

		ua.startBtn.Disable()
		ua.stopBtn.Enable()
		listenAddressEntry.Disable()
		targetPortEntry.Disable()
	})

	ua.stopBtn = widget.NewButton("停止服务器", func() {
		// 停止所有UDP中继服务器
		for port, relay := range ua.udpRelay {
			relay.Stop()
			delete(ua.udpRelay, port)
		}
		// 清空端口分配记录
		ua.reservePort = make(map[string]int)

		if ua.tcpService != nil {
			ua.tcpService.Stop()
			ua.tcpService = nil
		}

		ua.startBtn.Enable()
		ua.stopBtn.Disable()

		listenAddressEntry.Enable()
		targetPortEntry.Enable()
	})
	ua.stopBtn.Disable()

	// 客机模式按钮
	ua.connectBtn = widget.NewButton("连接服务器", func() {
		targetHost := strings.TrimSpace(targetHostEntry.Text)
		if targetHost == "" {
			ua.appendLog("错误：请输入目标主机地址")
			return
		}

		ua.tcpClient = tcp.NewTCPClient()
		ua.tcpClient.AddHandler(packer.ID_ServiceIdPackageID, func(m *easytcp.Message) {
			uploadBytesSize := new(int64)
			downloadBytesSize := new(int64)
			*uploadBytesSize = 0
			data := m.Data()
			*downloadBytesSize = int64(len(data) + 8)
			defer ua.UpdatePlayerTrafficByDeviceId("A", uploadBytesSize, downloadBytesSize)
			var respData packer.ServiceIdPackage
			if err := ua.tcpClient.Codec.Decode(data, &respData); err != nil {
				fyne.Do(func() {
					ua.appendLog(fmt.Sprintf("错误：解析服务ID包失败: %v", err))
				})
				return
			}

			ua.serviceId = respData.ServiceId
			authCode, deviceId, err := ua.configManager.GetServerByServiceId(respData.ServiceId)
			authenticationId := ""
			if err != nil {
				fyne.Do(func() {
					ua.appendLog(fmt.Sprintf("信息：连接到新服务器：%s", respData.ServiceId))
				})
			} else {
				encryptedAuthCode, err := EncryptWithPublicKey(respData.PublicKey, []byte(authCode))
				if err != nil {
					fyne.Do(func() {
						ua.appendLog(fmt.Sprintf("错误：加密认证码失败: %v", err))
					})
					deviceId = ""
				} else {
					authenticationId = base64.StdEncoding.EncodeToString(encryptedAuthCode)
				}
			}

			req := &packer.AuthenticationIdPackage{
				Index:            0,
				DeviceId:         deviceId,
				AuthenticationId: authenticationId,
			}
			sendBytesSize, err := ua.tcpClient.Send(packer.ID_AuthenticationIdPackageID, req)
			if err != nil {
				fyne.Do(func() {
					ua.appendLog(fmt.Sprintf("错误：发送认证码失败: %v", err))
				})
				return
			}
			*uploadBytesSize = int64(sendBytesSize)
			ua.playersMutex.Lock()
			defer ua.playersMutex.Unlock()
			ua.players[0].DeviceID = deviceId
			remark, err := ua.configManager.GetDeviceServiceRemark(deviceId, ua.serviceId)
			if err == nil {
				ua.players[0].Remark = remark
			} else {
				ua.players[0].Remark = ""
			}
			fyne.Do(func() {
				ua.refreshPlayerTableItem(1, 0)
			})
		})
		ua.tcpClient.AddHandler(packer.ID_ConfirmRegisterPackageID, func(m *easytcp.Message) {
			uploadBytesSize := new(int64)
			downloadBytesSize := new(int64)
			*uploadBytesSize = 0
			data := m.Data()
			*downloadBytesSize = int64(len(data) + 8)
			defer ua.UpdatePlayerTrafficByDeviceId("A", uploadBytesSize, downloadBytesSize)
			var respData packer.ConfirmRegisterPackage
			if err := ua.tcpClient.Codec.Decode(data, &respData); err != nil {
				fyne.Do(func() {
					ua.appendLog(fmt.Sprintf("错误：解析服务ID包失败: %v", err))
				})
				return
			}

			ua.configManager.InsertServer(ua.serviceId, respData.AuthenticationId, respData.DeviceId)
			remark, err := ua.configManager.GetDeviceServiceRemark(respData.DeviceId, ua.serviceId)
			ua.playersMutex.Lock()
			ua.players[0].DeviceID = respData.DeviceId
			if err == nil {
				ua.players[0].Remark = remark
			} else {
				ua.players[0].Remark = ""
			}
			ua.playersMutex.Unlock()
			fyne.Do(func() {
				ua.refreshPlayerTableItem(1, 0)
			})
		})
		ua.tcpClient.AddHandler(packer.ID_AllUserInfoPackageID, func(m *easytcp.Message) {
			uploadBytesSize := new(int64)
			downloadBytesSize := new(int64)
			*uploadBytesSize = 0
			data := m.Data()
			*downloadBytesSize = int64(len(data) + 8)
			defer ua.UpdatePlayerTrafficByDeviceId("A", uploadBytesSize, downloadBytesSize)
			var respData packer.AllUserInfoPackage
			if err := ua.tcpClient.Codec.Decode(data, &respData); err != nil {
				fyne.Do(func() {
					ua.appendLog(fmt.Sprintf("错误：解析服务ID包失败: %v", err))
				})
				return
			}

			// 根据服务器发送的玩家数据更新本地玩家列表
			ua.playersMutex.Lock()
			defer ua.playersMutex.Unlock()

			// 获取本地玩家的设备ID
			localDeviceId := ""
			if len(ua.players) > 0 {
				localDeviceId = ua.players[0].DeviceID
			}

			// 创建设备ID到玩家数据的映射，便于查找
			serverPlayers := make(map[string]*packer.AllUserInfoPackage_UserData)
			for _, userData := range respData.UserDataList {
				// 跳过本地玩家，避免重复添加
				if userData.DeviceId == localDeviceId {
					ua.players[0].Port = int(userData.Port)
					continue
				}
				serverPlayers[userData.DeviceId] = userData
			}

			// 处理本地玩家列表：删除、修改、新增
			var updatedPlayers []*Player

			// 首先保留本地玩家（索引0）
			if len(ua.players) > 0 {
				localPlayer := ua.players[0]
				updatedPlayers = append(updatedPlayers, localPlayer)
			}

			// 处理服务器返回的玩家数据
			for deviceId, serverPlayer := range serverPlayers {
				// 查找是否已存在该玩家
				found := false
				for i := 1; i < len(ua.players); i++ { // 从1开始，跳过本地玩家
					if ua.players[i].DeviceID == deviceId {
						// 更新现有玩家信息
						ua.players[i].Port = int(serverPlayer.Port)
						udpConfig := udp.DefaultConfig()
						udpConfig.Port = ua.players[i].Port
						// 尝试启动UDP中继
						relay := udp.NewUDPService(udpConfig)
						relay.SetDataCallback(func(data []byte, addr *net.UDPAddr) {
							uploadBytesSize := new(int64)
							downloadBytesSize := new(int64)
							*uploadBytesSize = 0
							*downloadBytesSize = 0
							defer ua.UpdatePlayerTrafficByDeviceId("A", uploadBytesSize, downloadBytesSize)
							ua.playersMutex.Lock()
							defer ua.playersMutex.Unlock()
							for index, player := range ua.players {
								if index == 0 {
									if player.Port != addr.Port {
										player.Port = addr.Port
										req := &packer.BindPortPackage{
											Port: int32(player.Port),
										}
										sendBytesSize, err := ua.tcpClient.Send(packer.ID_BindPortPackageID, req)
										if err != nil {
											fyne.Do(func() {
												ua.appendLog(fmt.Sprintf("错误：发送绑定端口失败: %v", err))
											})
											return
										}
										*uploadBytesSize = *uploadBytesSize + int64(sendBytesSize)
									}
								} else if player.Port == udpConfig.Port {
									req := &packer.ForwardPackage{
										Port:  int32(udpConfig.Port),
										Bytes: data,
									}
									sendBytesSize, err := ua.tcpClient.Send(packer.ID_ForwardPackageID, req)
									if err != nil {
										fyne.Do(func() {
											ua.appendLog(fmt.Sprintf("转发数据给 %d 失败: %v", udpConfig.Port, err))
										})
										return
									}
									player.TotalUpload += int64(sendBytesSize)
									ua.players[0].TotalUpload += int64(sendBytesSize)
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
						ua.udpRelay[ua.players[i].Port] = relay
						updatedPlayers = append(updatedPlayers, ua.players[i])
						found = true
						break
					}
				}

				if !found {
					// 新增玩家
					newPlayer := &Player{
						SessionID:     "",
						DeviceID:      deviceId,
						Remark:        "",
						Port:          int(serverPlayer.Port),
						TotalUpload:   0,
						TotalDownload: 0,
						LastUpload:    0,
						LastDownload:  0,
						UploadSpeed:   "0.00 B/s",
						DownloadSpeed: "0.00 B/s",
						Ping:          0,
					}

					// 尝试从数据库加载备注
					if ua.configManager != nil {
						remark, err := ua.configManager.GetDeviceServiceRemark(deviceId, ua.serviceId)
						if err == nil {
							newPlayer.Remark = remark
						}
					}

					updatedPlayers = append(updatedPlayers, newPlayer)
				}
			}

			// 更新玩家列表
			ua.players = updatedPlayers

			// 刷新表格显示
			fyne.Do(func() {
				ua.refreshPlayerTable()
			})
		})
		ua.tcpClient.AddHandler(packer.ID_ForwardPackageID, func(m *easytcp.Message) {
			ua.playersMutex.Lock()
			deviceId := "A"
			port := 0
			targetPort := 0
			data := m.Data()
			var respData packer.ForwardPackage
			if err := ua.tcpClient.Codec.Decode(data, &respData); err != nil {
				fyne.Do(func() {
					ua.appendLog(fmt.Sprintf("错误：解析服务ID包失败: %v", err))
				})
				return
			}
			for index, player := range ua.players {
				if index == 0 {
					targetPort = player.Port
				} else if player.Port == int(respData.Port) {
					deviceId = player.DeviceID
					port = player.Port
					break
				}
			}
			ua.playersMutex.Unlock()

			uploadBytesSize := new(int64)
			downloadBytesSize := new(int64)
			*uploadBytesSize = 0
			*downloadBytesSize = int64(len(data) + 8)
			defer ua.UpdatePlayerTrafficByDeviceId(deviceId, uploadBytesSize, downloadBytesSize)

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
		err := ua.tcpClient.Connect(targetHost)
		if err != nil {
			ua.appendLog(fmt.Sprintf("错误：连接服务器失败: %v", err))
			return
		}
		ua.playersMutex.Lock()
		ua.players = []*Player{
			{
				SessionID:     "",
				DeviceID:      "",
				Remark:        "",
				Port:          0,
				TotalUpload:   0,
				TotalDownload: 0,
				LastUpload:    0,
				LastDownload:  0,
				UploadSpeed:   "0.00 B/s",
				DownloadSpeed: "0.00 B/s",
				Ping:          0,
			},
		}
		ua.playersMutex.Unlock()
		ua.appendLog(fmt.Sprintf("信息：连接到服务器 %s", targetHost))
		ua.connectBtn.Disable()
		ua.disconnectBtn.Enable()
		targetHostEntry.Disable()
	})
	ua.disconnectBtn = widget.NewButton("断开服务器", func() {
		ua.tcpClient.Disconnect()
		ua.tcpClient = nil
		ua.appendLog("信息：断开服务器连接")
		ua.connectBtn.Enable()
		ua.disconnectBtn.Disable()
		targetHostEntry.Enable()
		targetPortEntry.Enable()
	})
	ua.disconnectBtn.Disable()

	clearBtn := widget.NewButton("清空日志", func() {
		ua.logText.SetText("")
	})

	// 创建玩家列表表格
	ua.createPlayerTable()
	playerTableScroll := container.NewScroll(ua.playerTable)
	playerTableScroll.SetMinSize(fyne.NewSize(550, 150))

	// 日志区域
	ua.logText = widget.NewMultiLineEntry()
	ua.logText.Disable()

	// 主机模式的内容
	hostContent := container.NewVBox(
		container.NewBorder(nil, nil, widget.NewLabel("监听端口"), nil, listenAddressEntry),
		container.NewBorder(nil, nil, widget.NewLabel("游戏端口"), nil, targetPortEntry),
		container.NewHBox(ua.startBtn, ua.stopBtn),
	)

	// 客机模式的内容
	clientContent := container.NewVBox(
		container.NewBorder(nil, nil, widget.NewLabel("目标主机"), nil, targetHostEntry),
		layout.NewSpacer(),
		container.NewHBox(ua.connectBtn, ua.disconnectBtn),
	)

	// 创建Tab容器
	tabs := container.NewAppTabs(
		container.NewTabItem("作为主机", hostContent),
		container.NewTabItem("作为客机", clientContent),
	)

	// 设置Tab切换回调
	tabs.OnSelected = func(ti *container.TabItem) {
		if ua.configManager != nil {
			// 保存当前选中的tab索引
			tabIndex := strconv.Itoa(tabs.SelectedIndex())
			ua.configManager.SetConfigDebounced("last_tab", tabIndex)
		}
	}

	// 恢复最后选择的tab
	if ua.configManager != nil {
		lastTab := ua.configManager.GetConfig("last_tab", "0")
		if tabIndex, err := strconv.Atoi(lastTab); err == nil && tabIndex >= 0 && tabIndex < len(tabs.Items) {
			tabs.SelectIndex(tabIndex)
		}
	}

	buttonRow := container.NewHBox(
		clearBtn,
		layout.NewSpacer(),
	)

	statusBox := container.NewVBox(
		buttonRow,
	)

	logScroll := container.NewScroll(ua.logText)
	logScroll.SetMinSize(fyne.NewSize(550, 150))

	topContent := container.NewVBox(
		widget.NewLabel("UDP 中继服务器"),
		tabs,
		statusBox,
		widget.NewSeparator(),
		widget.NewLabel("玩家列表"),
		playerTableScroll,
		widget.NewSeparator(),
		widget.NewLabel("运行日志"),
	)

	// 使用Border布局，将顶部内容放在North，日志区域放在Center以填充剩余空间
	content := container.NewBorder(topContent, nil, nil, nil, logScroll)
	ua.window.SetContent(content)
}

// 分配端口并启动UDP中继服务器
func (ua *UDPRelayApp) allocatePortAndStartRelay(deviceId string) (int, error) {
	// 检查是否已为该设备分配端口
	if port, exists := ua.reservePort[deviceId]; exists {
		// 检查UDP中继是否还在运行
		if relay, ok := ua.udpRelay[port]; ok && relay.IsRunning() {
			return port, nil
		}
		// 如果中继已停止，删除记录重新分配
		delete(ua.reservePort, deviceId)
		delete(ua.udpRelay, port)
	}

	udpConfig := udp.DefaultConfig()
	// 查找可用端口（从监听端口+1开始）
	startPort := ua.listenPort + 1
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
				ua.playersMutex.Lock()
				defer ua.playersMutex.Unlock()
				for index, player := range ua.players {
					if index == 0 {
						if player.Port != addr.Port {

						}
					} else if player.Port == localPort {
						req := &packer.ForwardPackage{
							Port:  int32(addr.Port),
							Bytes: data,
						}
						sendBytesSize, err := ua.tcpService.SendToSession(player.SessionID, packer.ID_ForwardPackageID, req)
						if err != nil {
							fyne.Do(func() {
								ua.appendLog(fmt.Sprintf("转发数据给 %s 失败: %v", player.DeviceID, err))
							})
							return
						}
						player.TotalUpload += int64(sendBytesSize)
						ua.players[0].TotalUpload += int64(sendBytesSize)
						break
					}
				}
			})

			// 记录分配的端口和UDP中继实例
			ua.reservePort[deviceId] = port
			ua.udpRelay[port] = relay

			ua.appendLog(fmt.Sprintf("为设备 %s 分配端口 %d，启动UDP中继", deviceId, port))
			return port, nil
		}
	}

	return 0, errors.New("无法找到可用端口")
}

// appendLog 添加日志到界面
func (ua *UDPRelayApp) appendLog(message string) {
	currentText := ua.logText.Text
	if currentText != "" {
		currentText += "\n"
	}
	currentText += message
	ua.logText.SetText(currentText)
	ua.logText.CursorRow = strings.Count(currentText, "\n")
}

// Run 运行应用程序
func (ua *UDPRelayApp) Run() {
	ua.createUI()

	// 恢复窗口状态
	ua.restoreWindowState()

	// 设置窗口关闭事件处理
	ua.window.SetCloseIntercept(func() {
		// 保存窗口状态
		ua.saveWindowState()
		// 关闭窗口
		ua.window.Close()
	})

	ua.window.ShowAndRun()
}

// 创建玩家表格
func (ua *UDPRelayApp) createPlayerTable() {
	ua.playerTable = widget.NewTable(
		func() (int, int) {
			ua.playersMutex.RLock()
			defer ua.playersMutex.RUnlock()
			return len(ua.players) + 2, 5 // 行数(玩家数+表头)，列数
		},
		func() fyne.CanvasObject {
			return widget.NewLabel("模板文本")
		},
		func(tci widget.TableCellID, co fyne.CanvasObject) {
			label := co.(*widget.Label)
			if tci.Row == 0 {
				// 表头
				switch tci.Col {
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

			ua.playersMutex.RLock()
			defer ua.playersMutex.RUnlock()

			if tci.Row-1 < len(ua.players) {
				player := ua.players[tci.Row-1]
				switch tci.Col {
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
		},
	)

	ua.playerTable.SetColumnWidth(0, 150) // 玩家列宽度
	ua.playerTable.SetColumnWidth(1, 80)  // 端口列宽度
	ua.playerTable.SetColumnWidth(2, 100) // 上传速度列宽度
	ua.playerTable.SetColumnWidth(3, 100) // 下载速度列宽度
	ua.playerTable.SetColumnWidth(4, 80)  // Ping列宽度
}

// startSpeedTimer 启动速度计算定时器
func (ua *UDPRelayApp) StartSpeedTimer() {
	ticker := time.NewTicker(1 * time.Second)
	go func() {
		for range ticker.C {
			ua.calculateAndUpdateSpeeds()
		}
	}()
}

// calculateAndUpdateSpeeds 计算并更新所有玩家的速度
func (ua *UDPRelayApp) calculateAndUpdateSpeeds() {
	ua.playersMutex.Lock()
	defer ua.playersMutex.Unlock()

	for row, player := range ua.players {
		// 计算上传速度
		uploadDiff := player.TotalUpload - player.LastUpload
		player.LastUpload = player.TotalUpload
		uploadSpeedStr := formatSpeed(uploadDiff)
		if player.UploadSpeed != uploadSpeedStr {
			player.UploadSpeed = uploadSpeedStr
			// 更新表格显示
			fyne.Do(func() {
				ua.refreshPlayerTableItem(row+1, 2) // +1是因为第0行是表头
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
				ua.refreshPlayerTableItem(row+1, 3) // +1是因为第0行是表头
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
	if uploadBytes == nil || downloadBytes == nil {
		return
	}

	ua.playersMutex.Lock()
	defer ua.playersMutex.Unlock()

	for index, player := range ua.players {
		if index == 0 || player.SessionID == sessionID {
			player.TotalUpload += *uploadBytes
			player.TotalDownload += *downloadBytes
		}
	}
}

// 根据DeviceId更新玩家流量统计
func (ua *UDPRelayApp) UpdatePlayerTrafficByDeviceId(deviceId string, uploadBytes *int64, downloadBytes *int64) {
	if uploadBytes == nil || downloadBytes == nil {
		return
	}

	ua.playersMutex.Lock()
	defer ua.playersMutex.Unlock()

	for index, player := range ua.players {
		if index == 0 || player.DeviceID == deviceId {
			player.TotalUpload += *uploadBytes
			player.TotalDownload += *downloadBytes
		}
	}
}

// 更新玩家表格显示
func (ua *UDPRelayApp) refreshPlayerTable() {
	if ua.playerTable != nil {
		ua.playerTable.Refresh()
	}
}

// 更新玩家表格显示
func (ua *UDPRelayApp) refreshPlayerTableItem(row int, col int) {
	if ua.playerTable != nil {
		ua.playerTable.RefreshItem(widget.TableCellID{Row: row, Col: col})
	}
}

// 保存窗口大小
func (ua *UDPRelayApp) saveWindowState() {
	if ua.configManager == nil {
		return
	}

	// 保存窗口大小
	size := ua.window.Content().Size()
	ua.configManager.SetConfigDebounced("window_width", strconv.Itoa(int(size.Width)))
	ua.configManager.SetConfigDebounced("window_height", strconv.Itoa(int(size.Height)))
}

// 恢复窗口大小
func (ua *UDPRelayApp) restoreWindowState() {
	if ua.configManager == nil {
		ua.window.Resize(fyne.NewSize(1024, 768))
		return
	}

	// 恢复窗口大小
	width, _ := strconv.Atoi(ua.configManager.GetConfig("window_width", "1024"))
	height, _ := strconv.Atoi(ua.configManager.GetConfig("window_height", "768"))
	ua.window.Resize(fyne.NewSize(float32(width), float32(height)))
}

// 在应用程序退出时关闭配置管理器
func (ua *UDPRelayApp) Cleanup() {
	if ua.configManager != nil {
		ua.configManager.Close()
	}
}

func main() {
	relayApp := NewUDPRelayApp()
	relayApp.StartSpeedTimer()
	relayApp.Run()
	relayApp.configManager.WaitForDebounced()
	// 清理资源
	relayApp.Cleanup()
}
