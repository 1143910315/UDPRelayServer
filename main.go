package main

import (
	"database/sql"
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

// TCPServiceRelay 表示一个TCP服务器
type TCPServiceRelay struct {
	tcpListener net.Listener
	logCallback func(string)
}

// NewTCPServiceRelay 创建一个新的TCP服务器实例
func NewTCPServiceRelay(listenPort int, logCallback func(string)) (*TCPServiceRelay, error) {
	// 创建TCP服务器
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", listenPort))
	if err != nil {
		return nil, err
	}
	r := &TCPServiceRelay{
		tcpListener: listener,
		logCallback: logCallback,
	}
	// 处理客户端连接
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				if !strings.Contains(err.Error(), "use of closed network connection") {
					r.logCallback(fmt.Sprintf("错误：接受连接错误: %v", err))
				}
				return
			}

			go r.handleTCPClient(conn)
		}
	}()

	return r, nil
}

// Stop 停止TCP中继服务器
func (r *TCPServiceRelay) Stop() {
	if r.tcpListener != nil {
		r.tcpListener.Close()
	}
	r.logCallback("TCP中继服务器已停止")
}

// handleMessages 处理接收到的TCP消息
func (r *TCPServiceRelay) handleTCPClient(conn net.Conn) {
	defer conn.Close()

	clientAddr := conn.RemoteAddr().String()
	r.logCallback(fmt.Sprintf("信息：TCP客户端连接: %s", clientAddr))

	// 准备要发送的数据
	message := "Hello!"
	messageBytes := []byte(message)
	messageLen := len(messageBytes)

	// 创建数据包：1字节消息ID + 4字节长度 + 字符串内容
	data := make([]byte, 1+4+messageLen)

	// 第一个字节为消息ID 0
	data[0] = 0

	// 接下来4个字节为字符串长度（大端序）
	data[1] = byte(messageLen >> 24)
	data[2] = byte(messageLen >> 16)
	data[3] = byte(messageLen >> 8)
	data[4] = byte(messageLen)

	// 最后是字符串内容
	copy(data[5:], messageBytes)

	// 发送数据包
	_, err := conn.Write(data)
	if err != nil {
		r.logCallback(fmt.Sprintf("错误：发送数据到TCP客户端 %s 失败: %v", clientAddr, err))
		return
	}
}

// TCPClientRelay 表示一个TCP客户端
type TCPClientRelay struct {
	tcpConnection net.Conn
	logCallback   func(string)
}

// NewTCPClientRelay 创建一个新的TCP客户端实例
func NewTCPClientRelay(targetHost string, logCallback func(string)) (*TCPClientRelay, error) {
	// 创建TCP客户端
	conn, err := net.Dial("tcp", targetHost)
	if err != nil {
		logCallback(fmt.Sprintf("连接失败: %v", err))
		return nil, err
	}
	r := &TCPClientRelay{
		tcpConnection: conn,
		logCallback:   logCallback,
	}

	// 处理TCP客户端接收数据
	go r.handleTCPClientData(conn)
	return r, nil
}

// 处理TCP客户端接收数据
func (r *TCPClientRelay) handleTCPClientData(conn net.Conn) {
	defer conn.Close()

	buffer := make([]byte, 4096)

	for {
		n, err := conn.Read(buffer)
		if err != nil {
			if !strings.Contains(err.Error(), "use of closed network connection") {
				r.logCallback(fmt.Sprintf("TCP客户端接收错误: %v", err))
			}
			break
		}

		if n > 0 {
			// 第一个字节为消息id
			messageID := buffer[0]
			data := buffer[1:n]
			// r.logCallback(fmt.Sprintf("收到消息 ID: %d, 数据长度: %d 字节", messageID, len(data)))

			// 根据不同的消息ID进行不同处理
			switch messageID {
			case 0:
				// 处理消息ID为0的数据
				r.handleMessageID0(data)
			case 1:
				// 处理消息ID为1的数据
				r.handleMessageID1(data)
			default:
				r.logCallback(fmt.Sprintf("错误：未知消息ID: %d", messageID))
			}
		}
	}
	r.logCallback("信息：TCP客户端连接已断开")
}

// 处理消息ID为0的数据
func (r *TCPClientRelay) handleMessageID0(data []byte) {
	// 消息ID 0 是字符串消息
	if len(data) >= 4 {
		// 读取长度字段（大端序）
		messageLen := int(uint32(data[0])<<24 | uint32(data[1])<<16 | uint32(data[2])<<8 | uint32(data[3]))
		if len(data) >= 4+messageLen {
			message := string(data[4 : 4+messageLen])
			r.logCallback(fmt.Sprintf("消息0内容: %s", message))
		}
	} else {
		r.logCallback("错误：消息ID0数据长度不足4字节")
	}
}

// 处理消息ID为1的数据
func (r *TCPClientRelay) handleMessageID1(data []byte) {
	// 处理其他类型的消息
	r.logCallback(fmt.Sprintf("收到消息1，数据长度: %d", len(data)))
}

// GUI应用程序结构
type UDPRelayApp struct {
	app             fyne.App
	window          fyne.Window
	udpRelay        *UDPRelay
	tcpServiceRelay *TCPServiceRelay
	logText         *widget.Entry
	startBtn        *widget.Button
	stopBtn         *widget.Button
	connectBtn      *widget.Button
	disconnectBtn   *widget.Button
	configManager   *ConfigManager
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
			ua.configManager.SetConfigDebounced("listen_port", text)
		}
		targetHostEntry.OnChanged = func(text string) {
			ua.configManager.SetConfigDebounced("target_host", text)
		}
		targetPortEntry.OnChanged = func(text string) {
			ua.configManager.SetConfigDebounced("target_port", text)
		}
	} else {
		listenPortEntry.SetText("8080")
		targetHostEntry.SetText("127.0.0.1")
		targetPortEntry.SetText("8081")
	}

	listenPortEntry.SetPlaceHolder("监听端口")
	targetHostEntry.SetPlaceHolder("目标主机")
	targetPortEntry.SetPlaceHolder("游戏端口")

	// 主机模式按钮
	ua.startBtn = widget.NewButton("启动服务器", func() {
		listenPort, err := strconv.Atoi(listenPortEntry.Text)
		if err != nil || listenPort < 1 || listenPort > 65535 {
			ua.appendLog("错误: 监听端口必须是 1-65535 之间的数字")
			return
		}

		ua.tcpServiceRelay, err = NewTCPServiceRelay(listenPort, func(s string) {
			fyne.Do(func() {
				ua.appendLog(s)
			})
		})
		if err != nil {
			ua.appendLog(fmt.Sprintf("启动TCP服务器失败: %v", err))
			return
		}

		ua.appendLog(fmt.Sprintf("信息：TCP服务器启动成功，监听端口 %d", listenPort))

		ua.startBtn.Disable()
		ua.stopBtn.Enable()
		listenPortEntry.Disable()
		targetHostEntry.Disable()
		targetPortEntry.Disable()
	})

	ua.stopBtn = widget.NewButton("停止服务器", func() {
		if ua.udpRelay != nil {
			ua.udpRelay.Stop()
			ua.udpRelay = nil
		}

		if ua.tcpServiceRelay != nil {
			ua.tcpServiceRelay.Stop()
			ua.tcpServiceRelay = nil
		}

		ua.startBtn.Enable()
		ua.stopBtn.Disable()
		listenPortEntry.Enable()
		targetHostEntry.Enable()
		targetPortEntry.Enable()
	})
	ua.stopBtn.Disable()

	// 客机模式按钮
	ua.connectBtn = widget.NewButton("连接服务器", nil)
	ua.disconnectBtn = widget.NewButton("断开服务器", nil)
	ua.disconnectBtn.Disable()

	clearBtn := widget.NewButton("清空日志", func() {
		ua.logText.SetText("")
	})

	// 客机模式按钮事件处理（暂时为空实现）
	ua.connectBtn.OnTapped = func() {
		targetHost := strings.TrimSpace(targetHostEntry.Text)
		if targetHost == "" {
			ua.appendLog("错误: 请输入目标主机地址")
			return
		}

		// TODO: 实现客机连接逻辑
		ua.appendLog(fmt.Sprintf("连接到服务器 %s", targetHost))
		ua.connectBtn.Disable()
		ua.disconnectBtn.Enable()
		targetHostEntry.Disable()
		targetPortEntry.Disable()
	}

	ua.disconnectBtn.OnTapped = func() {
		// TODO: 实现客机断开逻辑
		ua.appendLog("断开服务器连接")
		ua.connectBtn.Enable()
		ua.disconnectBtn.Disable()
		targetHostEntry.Enable()
		targetPortEntry.Enable()
	}

	// 创建玩家列表表格
	ua.createPlayerTable()
	playerTableScroll := container.NewScroll(ua.playerTable)
	playerTableScroll.SetMinSize(fyne.NewSize(550, 150))

	// 日志区域
	ua.logText = widget.NewMultiLineEntry()
	ua.logText.SetText("日志将显示在这里...")
	ua.logText.Disable()

	// 主机模式的内容
	hostContent := container.NewVBox(
		container.NewBorder(nil, nil, widget.NewLabel("监听端口"), nil, listenPortEntry),
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
