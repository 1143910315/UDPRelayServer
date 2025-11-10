package main

import (
	"database/sql"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"

	_ "github.com/mattn/go-sqlite3"
)

// 玩家结构体
type Player struct {
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
	db    *sql.DB
	mutex sync.RWMutex
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

	return &ConfigManager{db: db}, nil
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

// GUI应用程序结构
type UDPRelayApp struct {
	app           fyne.App
	window        fyne.Window
	relay         *UDPRelay
	statusLabel   *widget.Label
	logText       *widget.Entry
	startBtn      *widget.Button
	stopBtn       *widget.Button
	configManager *ConfigManager
	// 玩家列表控件
	playerTable *widget.Table
	// 玩家列表
	players      []*Player
	playersMutex sync.RWMutex
}

// 在应用程序退出时关闭配置管理器
func (ua *UDPRelayApp) Cleanup() {
	if ua.configManager != nil {
		ua.configManager.Close()
	}
}

// NewUDPRelayApp 创建新的GUI应用程序
func NewUDPRelayApp() *UDPRelayApp {
	myApp := app.New()
	window := myApp.NewWindow("UDP 中继服务器")
	window.Resize(fyne.NewSize(1024, 768))

	// 初始化配置管理器
	configManager, err := NewConfigManager()
	if err != nil {
		// 如果配置管理器初始化失败，使用默认值继续运行
		fmt.Printf("配置管理器初始化失败: %v\n", err)
	}

	return &UDPRelayApp{
		app:           myApp,
		window:        window,
		configManager: configManager,
	}
}

// createUI 创建用户界面
func (ua *UDPRelayApp) createUI() {
	// 创建输入字段
	listenPortEntry := widget.NewEntry()
	targetHostEntry := widget.NewEntry()
	targetPortEntry := widget.NewEntry()

	// 从数据库加载配置，如果失败则使用默认值
	if ua.configManager != nil {
		listenPortEntry.SetText(ua.configManager.GetConfig("listen_port", "8080"))
		targetHostEntry.SetText(ua.configManager.GetConfig("target_host", "127.0.0.1"))
		targetPortEntry.SetText(ua.configManager.GetConfig("target_port", "8081"))

		// 设置配置保存回调
		listenPortEntry.OnChanged = func(text string) {
			ua.configManager.SetConfig("listen_port", text)
		}
		targetHostEntry.OnChanged = func(text string) {
			ua.configManager.SetConfig("target_host", text)
		}
		targetPortEntry.OnChanged = func(text string) {
			ua.configManager.SetConfig("target_port", text)
		}
	} else {
		listenPortEntry.SetText("8080")
		targetHostEntry.SetText("127.0.0.1")
		targetPortEntry.SetText("8081")
	}

	listenPortEntry.SetPlaceHolder("监听端口")
	targetHostEntry.SetPlaceHolder("目标主机")
	targetPortEntry.SetPlaceHolder("目标端口")

	// 状态标签
	ua.statusLabel = widget.NewLabel("状态: 未启动")
	ua.statusLabel.Alignment = fyne.TextAlignCenter

	// 创建玩家列表表格
	ua.createPlayerTable()

	// 日志区域
	ua.logText = widget.NewMultiLineEntry()
	ua.logText.SetPlaceHolder("日志将显示在这里...")
	ua.logText.Disable()

	// 按钮
	ua.startBtn = widget.NewButton("启动服务器", nil)
	ua.stopBtn = widget.NewButton("停止服务器", nil)
	ua.stopBtn.Disable()

	clearBtn := widget.NewButton("清空日志", func() {
		ua.logText.SetText("")
	})

	// 按钮事件处理
	ua.startBtn.OnTapped = func() {
		listenPort, err := strconv.Atoi(listenPortEntry.Text)
		if err != nil || listenPort < 1 || listenPort > 65535 {
			ua.appendLog("错误: 监听端口必须是 1-65535 之间的数字")
			return
		}

		targetHost := strings.TrimSpace(targetHostEntry.Text)
		if targetHost == "" {
			ua.appendLog("错误: 请输入目标主机地址")
			return
		}

		targetPort, err := strconv.Atoi(targetPortEntry.Text)
		if err != nil || targetPort < 1 || targetPort > 65535 {
			ua.appendLog("错误: 目标端口必须是 1-65535 之间的数字")
			return
		}

		// 创建中继服务器实例
		ua.relay = NewUDPRelay(listenPort, targetHost, targetPort, ua.appendLog)

		// 启动服务器
		err = ua.relay.Start()
		if err != nil {
			ua.appendLog(fmt.Sprintf("启动失败: %v", err))
			return
		}

		ua.statusLabel.SetText(fmt.Sprintf("状态: 运行中 - 监听 :%d -> %s:%d",
			listenPort, targetHost, targetPort))
		ua.startBtn.Disable()
		ua.stopBtn.Enable()
		listenPortEntry.Disable()
		targetHostEntry.Disable()
		targetPortEntry.Disable()
	}

	ua.stopBtn.OnTapped = func() {
		if ua.relay != nil {
			ua.relay.Stop()
			ua.relay = nil
		}

		ua.statusLabel.SetText("状态: 已停止")
		ua.startBtn.Enable()
		ua.stopBtn.Disable()
		listenPortEntry.Enable()
		targetHostEntry.Enable()
		targetPortEntry.Enable()
	}

	// 创建布局
	inputForm := container.NewVBox(
		widget.NewForm(
			widget.NewFormItem("监听端口", listenPortEntry),
			widget.NewFormItem("目标主机", targetHostEntry),
			widget.NewFormItem("目标端口", targetPortEntry),
		),
	)

	buttonRow := container.NewHBox(
		ua.startBtn,
		ua.stopBtn,
		clearBtn,
		layout.NewSpacer(),
	)

	statusBox := container.NewVBox(
		ua.statusLabel,
	)

	logScroll := container.NewScroll(ua.logText)
	logScroll.SetMinSize(fyne.NewSize(400, 150))

	topContent := container.NewVBox(
		widget.NewLabel("UDP 中继服务器配置"),
		inputForm,
		buttonRow,
		statusBox,
		widget.NewSeparator(),
		widget.NewLabel("玩家列表"),
		container.NewScroll(ua.playerTable),
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
	ua.window.ShowAndRun()
}

// 创建玩家表格
func (ua *UDPRelayApp) createPlayerTable() {
	ua.playerTable = widget.NewTable(
		func() (int, int) {
			ua.playersMutex.RLock()
			defer ua.playersMutex.RUnlock()
			return len(ua.players) + 1, 5 // 行数(玩家数+表头)，列数
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
	}
}

func main() {
	relayApp := NewUDPRelayApp()
	relayApp.Run()
	relayApp.Cleanup()
}
