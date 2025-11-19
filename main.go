package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
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

	"github.com/1143910315/UDPRelayServer/net/tcp"
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
	sessionID         string
	ID                string
	Remark            string
	Port              int
	TotalUpload       int64
	TotalDownload     int64
	LastUpload        int64
	LastDownload      int64
	Ping              int
	LastSpeedCalcTime time.Time
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
	createTableSQL := `
    CREATE TABLE IF NOT EXISTS app_config (
        key TEXT PRIMARY KEY,
        value TEXT NOT NULL
    );`

	_, err = db.Exec(createTableSQL)
	if err != nil {
		return nil, err
	}

	// 创建密钥表
	createKeyTableSQL := `
	CREATE TABLE IF NOT EXISTS key_pairs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		service_id TEXT NOT NULL UNIQUE,
		private_key TEXT NOT NULL,
		public_key TEXT NOT NULL
	);`

	_, err = db.Exec(createKeyTableSQL)
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

	_, err := cm.db.Exec(`
        INSERT OR REPLACE INTO app_config (key, value) 
        VALUES (?, ?)`, key, value)
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

// 关闭数据库连接
func (cm *ConfigManager) Close() error {
	return cm.db.Close()
}

// 使用WaitGroup等待
func (cm *ConfigManager) WaitForDebounced() {
	// 等待所有正在执行的防抖操作完成
	cm.debounceWg.Wait()
}

// UDPRelay 表示一个UDP中继服务器
type UDPRelay struct {
	listenPort   int
	targetHost   string
	targetPort   int
	isRunning    bool
	conn         *net.UDPConn
	clients      map[string]*net.UDPAddr
	clientsMutex sync.RWMutex
	logCallback  func(string)
}

// NewUDPRelay 创建一个新的UDP中继实例
func NewUDPRelay(listenPort int, targetHost string, targetPort int, logCallback func(string)) *UDPRelay {
	return &UDPRelay{
		listenPort:  listenPort,
		targetHost:  targetHost,
		targetPort:  targetPort,
		clients:     make(map[string]*net.UDPAddr),
		logCallback: logCallback,
	}
}

// Start 启动UDP中继服务器
func (r *UDPRelay) Start() error {
	addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf(":%d", r.listenPort))
	if err != nil {
		return fmt.Errorf("解析监听地址失败: %v", err)
	}

	r.conn, err = net.ListenUDP("udp", addr)
	if err != nil {
		return fmt.Errorf("启动UDP服务器失败: %v", err)
	}

	r.isRunning = true
	r.log(fmt.Sprintf("UDP中继服务器启动成功，监听端口 %d，目标地址 %s:%d",
		r.listenPort, r.targetHost, r.targetPort))

	go r.handleMessages()
	return nil
}

// Stop 停止UDP中继服务器
func (r *UDPRelay) Stop() {
	if r.conn != nil {
		r.conn.Close()
		r.conn = nil
	}
	r.isRunning = false
	r.clientsMutex.Lock()
	r.clients = make(map[string]*net.UDPAddr)
	r.clientsMutex.Unlock()
	r.log("UDP中继服务器已停止")
}

// handleMessages 处理接收到的UDP消息
func (r *UDPRelay) handleMessages() {
	buffer := make([]byte, 4096)

	for r.isRunning {
		n, clientAddr, err := r.conn.ReadFromUDP(buffer)
		if err != nil {
			if r.isRunning {
				r.log(fmt.Sprintf("读取数据错误: %v", err))
			}
			continue
		}

		// 记录客户端
		clientKey := clientAddr.String()
		r.clientsMutex.Lock()
		r.clients[clientKey] = clientAddr
		clientsCount := len(r.clients)
		r.clientsMutex.Unlock()

		r.log(fmt.Sprintf("收到来自 %s 的数据: %d 字节, 活跃客户端: %d",
			clientKey, n, clientsCount))

		// 转发到目标服务器
		go r.forwardToTarget(buffer[:n], clientAddr)
	}
}

// forwardToTarget 将数据转发到目标服务器
func (r *UDPRelay) forwardToTarget(data []byte, clientAddr *net.UDPAddr) {
	targetAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", r.targetHost, r.targetPort))
	if err != nil {
		r.log(fmt.Sprintf("解析目标地址失败: %v", err))
		return
	}

	targetConn, err := net.DialUDP("udp", nil, targetAddr)
	if err != nil {
		r.log(fmt.Sprintf("连接目标服务器失败: %v", err))
		return
	}
	defer targetConn.Close()

	_, err = targetConn.Write(data)
	if err != nil {
		r.log(fmt.Sprintf("发送数据到目标服务器失败: %v", err))
		return
	}

	// 接收目标服务器的响应
	response := make([]byte, 4096)
	targetConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := targetConn.Read(response)
	if err != nil {
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			// 读取超时是正常的，不一定有响应
			return
		}
		r.log(fmt.Sprintf("读取目标服务器响应失败: %v", err))
		return
	}

	// 将响应返回给客户端
	if r.conn != nil {
		_, err = r.conn.WriteToUDP(response[:n], clientAddr)
		if err != nil {
			r.log(fmt.Sprintf("返回响应给客户端失败: %v", err))
		} else {
			r.log(fmt.Sprintf("转发响应给 %s: %d 字节", clientAddr.String(), n))
		}
	}
}

// GetClientCount 获取当前客户端数量
func (r *UDPRelay) GetClientCount() int {
	r.clientsMutex.RLock()
	defer r.clientsMutex.RUnlock()
	return len(r.clients)
}

// log 记录日志
func (r *UDPRelay) log(message string) {
	if r.logCallback != nil {
		timestamp := time.Now().Format("2006-01-02 15:04:05")
		r.logCallback(fmt.Sprintf("[%s] %s", timestamp, message))
	}
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

// 保存密钥对到数据库
func (cm *ConfigManager) SaveKeyPair(serviceId, privateKey, publicKey string) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	_, err := cm.db.Exec(`
		INSERT OR REPLACE INTO key_pairs (service_id, private_key, public_key) 
		VALUES (?, ?, ?)`, serviceId, privateKey, publicKey)
	return err
}

// 从数据库获取密钥对
func (cm *ConfigManager) GetKeyPairByID(id int) (string, string, string, error) {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	var serviceId, privateKey, publicKey string
	err := cm.db.QueryRow("SELECT service_id, private_key, public_key FROM key_pairs WHERE id = ?", id).
		Scan(&serviceId, &privateKey, &publicKey)
	if err != nil {
		return "", "", "", err
	}
	return serviceId, privateKey, publicKey, nil
}

// GUI应用程序结构
type UDPRelayApp struct {
	app           fyne.App
	window        fyne.Window
	udpRelay      *UDPRelay
	tcpService    *tcp.TCPServer
	tcpClient     *tcp.TCPClient
	logText       *widget.Entry
	startBtn      *widget.Button
	stopBtn       *widget.Button
	connectBtn    *widget.Button
	disconnectBtn *widget.Button
	configManager *ConfigManager
	// 玩家列表控件
	playerTable *widget.Table
	// 玩家列表
	players      []*Player
	playersMutex sync.RWMutex
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
		app:           myApp,
		window:        window,
		configManager: configManager,
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
		targetHostEntry.SetText(ua.configManager.GetConfig("target_host", "127.0.0.1"))
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
		targetHostEntry.SetText("127.0.0.1")
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

		ua.tcpService = tcp.NewTCPServer()
		ua.tcpService.OnClientConnected = func(sessionID string) {
			ua.playersMutex.Lock()
			defer ua.playersMutex.Unlock()

			ua.players = append(ua.players, &Player{
				sessionID:         sessionID,
				ID:                "",
				Remark:            "", // 初始备注为空
				Port:              0,  // 初始端口为0，后续可更新
				TotalUpload:       0,
				TotalDownload:     0,
				LastUpload:        0,
				LastDownload:      0,
				Ping:              0,
				LastSpeedCalcTime: time.Now(),
			})
			playersSize := len(ua.players)
			fyne.Do(func() {
				ua.appendLog(fmt.Sprintf("客户端连接: %s", sessionID))
				ua.refreshPlayerTableForRow(playersSize) // 刷新表格显示
			})
		}
		err = ua.tcpService.Start(":" + listenAddress)
		if err != nil {
			ua.appendLog(fmt.Sprintf("启动TCP服务器失败: %v", err))
			return
		}

		ua.appendLog(fmt.Sprintf("信息：TCP服务器启动成功，监听 %s", listenAddress))

		ua.startBtn.Disable()
		ua.stopBtn.Enable()
		listenAddressEntry.Disable()
		targetPortEntry.Disable()
	})

	ua.stopBtn = widget.NewButton("停止服务器", func() {
		if ua.udpRelay != nil {
			ua.udpRelay.Stop()
			ua.udpRelay = nil
		}

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
		err := ua.tcpClient.Connect(targetHost)
		if err != nil {
			ua.appendLog(fmt.Sprintf("错误：连接服务器失败: %v", err))
			return
		}

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
			return widget.NewLabel("")
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
						label.SetText(player.ID)
					}
				case 1:
					label.SetText(strconv.Itoa(player.Port))
				case 2:
					// 计算上传速度
					speed := ua.calculateUploadSpeed(player)
					label.SetText(fmt.Sprintf("%.2f KB/s", speed))
				case 3:
					// 计算下载速度
					speed := ua.calculateDownloadSpeed(player)
					label.SetText(fmt.Sprintf("%.2f KB/s", speed))
				case 4:
					label.SetText(strconv.Itoa(player.Ping) + "ms")
				}
			}
		},
	)

	ua.playerTable.SetColumnWidth(0, 150) // 玩家列宽度
	ua.playerTable.SetColumnWidth(1, 80)  // 端口列宽度
	ua.playerTable.SetColumnWidth(2, 100) // 上传速度列宽度
	ua.playerTable.SetColumnWidth(3, 100) // 下载速度列宽度
	ua.playerTable.SetColumnWidth(4, 80)  // Ping列宽度
}

// 计算上传速度
func (ua *UDPRelayApp) calculateUploadSpeed(player *Player) float64 {
	if time.Since(player.LastSpeedCalcTime).Seconds() < 1 {
		return 0
	}

	speed := float64(player.TotalUpload-player.LastUpload) / 1024 /
		time.Since(player.LastSpeedCalcTime).Seconds()

	player.LastUpload = player.TotalUpload
	player.LastSpeedCalcTime = time.Now()

	return speed
}

// 计算下载速度
func (ua *UDPRelayApp) calculateDownloadSpeed(player *Player) float64 {
	if time.Since(player.LastSpeedCalcTime).Seconds() < 1 {
		return 0
	}

	speed := float64(player.TotalDownload-player.LastDownload) / 1024 /
		time.Since(player.LastSpeedCalcTime).Seconds()

	player.LastDownload = player.TotalDownload
	player.LastSpeedCalcTime = time.Now()

	return speed
}

// 更新玩家表格显示
func (ua *UDPRelayApp) refreshPlayerTable() {
	if ua.playerTable != nil {
		ua.playerTable.Refresh()
		// ua.playerTable.UpdateCell
	}
}

// 更新玩家表格显示
func (ua *UDPRelayApp) refreshPlayerTableForRow(row int) {
	if ua.playerTable != nil {
		ua.playerTable.RefreshItem(widget.TableCellID{Row: row, Col: 0})
		ua.playerTable.RefreshItem(widget.TableCellID{Row: row, Col: 1})
		ua.playerTable.RefreshItem(widget.TableCellID{Row: row, Col: 2})
		ua.playerTable.RefreshItem(widget.TableCellID{Row: row, Col: 3})
		ua.playerTable.RefreshItem(widget.TableCellID{Row: row, Col: 4})
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
	relayApp.Run()
	relayApp.configManager.WaitForDebounced()
	// 清理资源
	relayApp.Cleanup()
}
