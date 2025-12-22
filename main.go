package main

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/1143910315/UDPRelayServer/internal/config"
	"github.com/1143910315/UDPRelayServer/internal/game/data"
	"github.com/1143910315/UDPRelayServer/internal/game/session"
	"github.com/1143910315/UDPRelayServer/internal/gui"
	"github.com/1143910315/UDPRelayServer/internal/gui/ui"
	"github.com/1143910315/UDPRelayServer/internal/security"
	"github.com/1143910315/UDPRelayServer/internal/utils"
	"github.com/google/uuid"
)

// GUI应用程序结构
type UDPRelayApp struct {
	app                       fyne.App
	mainPage                  *ui.MainPage
	hostSession               *session.HostSession
	clientSession             *session.ClientSession
	configManager             *config.ConfigManager
	advancedStringIncrementer *utils.AdvancedStringIncrementer
	serviceID                 string
	appendLog                 func(level, message string)
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

	ua := &UDPRelayApp{
		app:                       myApp,
		mainPage:                  ui.NewMainPage(myApp),
		hostSession:               session.NewHostSession(),
		clientSession:             session.NewClientSession(),
		configManager:             configManager,
		advancedStringIncrementer: utils.NewAdvancedStringIncrementer(),
	}
	ua.appendLog = func(level, message string) {
		switch level {
		case "debug":
			message = fmt.Sprintf("调试: %s", message)
		case "info":
			message = fmt.Sprintf("信息: %s", message)
		case "warn":
			message = fmt.Sprintf("警告: %s", message)
		case "error":
			message = fmt.Sprintf("错误: %s", message)
		default:
			message = fmt.Sprintf("未知: %s", message)
		}
		currentText := ua.mainPage.LogText.Text
		if currentText != "" {
			currentText += "\n"
		}
		currentText += message
		ua.mainPage.LogText.SetText(currentText)
		ua.mainPage.LogText.CursorRow = strings.Count(currentText, "\n")
	}
	return ua, nil
}

func (ua *UDPRelayApp) initData() {
	options, err := ua.configManager.GetAllConnectionAddresses()
	if err != nil {
		ua.appendLog("error", fmt.Sprintf("获取连接地址失败: %v", err))
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
	ua.mainPage.SetIdleStatus()
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
			ua.appendLog("error", "监听端口必须是 1-65535 之间的数字")
			return
		}
		targetPort, err := strconv.Atoi(strings.TrimSpace(ua.mainPage.TargetPortEntry.Text))
		if err != nil || targetPort < 1 || targetPort > 65535 {
			ua.appendLog("error", "游戏端口必须是 1-65535 之间的数字")
			return
		}
		err = ua.hostSession.Start(":"+listenPortText, targetPort)
		if err != nil {
			ua.appendLog("error", fmt.Sprintf("启动TCP服务器失败: %v", err))
			return
		}
		ua.mainPage.SetHostStatus()
		ua.mainPage.RefreshPlayerTable()
		ua.appendLog("info", fmt.Sprintf("信息：TCP服务器启动成功，监听 %s", listenPortText))
	}
	ua.mainPage.StopBtn.OnTapped = func() {
		ua.hostSession.Stop()
		ua.mainPage.SetIdleStatus()
		ua.mainPage.RefreshPlayerTable()
		ua.appendLog("info", "信息：TCP服务器已停止")
	}
	ua.mainPage.ConnectBtn.OnTapped = func() {
		targetHost := strings.TrimSpace(ua.mainPage.TargetHostEntry.Text)
		if targetHost == "" {
			ua.appendLog("error", "请输入目标主机地址")
			return
		}

		err := ua.clientSession.Connect(targetHost)
		if err != nil {
			ua.appendLog("error", fmt.Sprintf("连接到服务器失败: %v", err))
			return
		}
		err = ua.configManager.SetConnectionAddressAsLastUsed(targetHost)
		if err != nil {
			ua.appendLog("error", fmt.Sprintf("保存连接地址失败: %v", err))
		}
		ua.mainPage.SetClientStatus()
	}
	ua.mainPage.DisconnectBtn.OnTapped = func() {
		ua.clientSession.Disconnect()
		ua.mainPage.SetIdleStatus()
		ua.mainPage.RefreshPlayerTable()
	}
	ua.mainPage.ClearBtn.OnTapped = func() {
		ua.mainPage.LogText.SetText("")
	}
	ua.mainPage.ClearBtn1.OnTapped = ua.mainPage.ClearBtn.OnTapped
	ua.mainPage.PlayerTable.CreateCell = func() fyne.CanvasObject {
		return widget.NewLabel("模板文本")
	}
	ua.mainPage.PlayerTable.Length = func() (int, int) {
		if !ua.mainPage.StopBtn.Disabled() {
			ua.hostSession.PlayerManager.PlayersMutex.RLock()
			defer ua.hostSession.PlayerManager.PlayersMutex.RUnlock()
			return len(ua.hostSession.PlayerManager.Players) + 2, 5
		} else if !ua.mainPage.DisconnectBtn.Disabled() {
			ua.clientSession.PlayerManager.PlayersMutex.RLock()
			defer ua.clientSession.PlayerManager.PlayersMutex.RUnlock()
			return len(ua.clientSession.PlayerManager.Players) + 2, 5
		} else {
			return 2, 5
		}
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
		var playerManager *data.PlayerManager
		if !ua.mainPage.StopBtn.Disabled() {
			playerManager = ua.hostSession.PlayerManager

		} else if !ua.mainPage.DisconnectBtn.Disabled() {
			playerManager = ua.clientSession.PlayerManager
		} else {
			label.SetText("")
			return
		}
		playerManager.PlayersMutex.RLock()
		defer playerManager.PlayersMutex.RUnlock()

		if id.Row-1 < len(playerManager.Players) {
			player := playerManager.Players[id.Row-1]
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
	ua.hostSession.OnNeedGenerateAuthentication = func(sessionID string) (authenticationID string, deviceID string) {
		uuidObj, _ := uuid.NewUUID()
		return uuidObj.String(), ua.advancedStringIncrementer.IncrementString(ua.configManager.GetConfig("max_generated_device_id", "A"))
	}
	ua.hostSession.OnPlayerInfoUpdated = func() {
		fyne.Do(func() {
			ua.mainPage.RefreshPlayerTable()
		})
	}
	ua.hostSession.OnLog = func(level, message string) {
		fyne.Do(func() {
			ua.appendLog(level, message)
		})
	}
	ua.hostSession.GetDeviceRemark = func(deviceID string) string {
		remark, err := ua.configManager.GetDeviceServiceRemark(deviceID, "")
		if err != nil {
			return ""
		}
		return remark
	}
	ua.hostSession.GetKeyPairByServiceIDIndex = func(serviceIDIndex int) (serviceId string, publicKey string, err error) {
		serviceID, _, publicKey, err := ua.configManager.GetKeyPairByID(serviceIDIndex)
		return serviceID, publicKey, err
	}
	ua.hostSession.CheckDeviceAuthentication = func(serviceIDIndex int, deviceID, authenticationID string) bool {
		_, privateKey, _, err := ua.configManager.GetKeyPairByID(serviceIDIndex)
		if err == nil {
			encryptedData, err := base64.StdEncoding.DecodeString(authenticationID)
			if err == nil {
				authenticationID, err := security.DecryptWithPrivateKey(privateKey, encryptedData)
				if err == nil {
					authentication, err := ua.configManager.GetDeviceAuthentication(deviceID)
					if err == nil {
						return string(authenticationID) == authentication
					}
				}
			}
		}
		return false
	}
	ua.clientSession.OnLog = ua.hostSession.OnLog
	ua.clientSession.OnPlayerInfoUpdated = func() {
		fyne.Do(func() {
			ua.mainPage.RefreshPlayerTable()
		})
	}
	ua.clientSession.GetDeviceServiceRemark = func(deviceID, serviceID string) string {
		remark, err := ua.configManager.GetDeviceServiceRemark(deviceID, serviceID)
		if err != nil {
			return ""
		}
		return remark
	}
	ua.clientSession.GetServerByServiceID = func(serviceID string) (authCode string, deviceID string, err error) {
		return ua.configManager.GetServerByServiceID(serviceID)
	}
	ua.clientSession.InsertServer = func(serviceID, authCode, deviceID string) error {
		return ua.configManager.InsertServer(serviceID, authCode, deviceID)
	}
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
	var playerManager *data.PlayerManager
	if !ua.mainPage.StopBtn.Disabled() {
		playerManager = ua.hostSession.PlayerManager
	} else if !ua.mainPage.DisconnectBtn.Disabled() {
		playerManager = ua.clientSession.PlayerManager
	} else {
		return
	}
	playerManager.PlayersMutex.Lock()
	defer playerManager.PlayersMutex.Unlock()

	for row, player := range playerManager.Players {
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

// 格式化速度显示，保留两位小数并自动匹配单位
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
