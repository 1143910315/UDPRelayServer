package main

import (
    "fmt"
    "net"
    "strconv"
    "strings"
    "sync"
    "time"

    "fyne.io/fyne/v2"
    "fyne.io/fyne/v2/app"
    "fyne.io/fyne/v2/container"
    "fyne.io/fyne/v2/widget"
    "fyne.io/fyne/v2/layout"
)

// UDPRelay 表示一个UDP中继服务器
type UDPRelay struct {
    listenPort    int
    targetHost    string
    targetPort    int
    isRunning     bool
    conn          *net.UDPConn
    clients       map[string]*net.UDPAddr
    clientsMutex  sync.RWMutex
    logCallback   func(string)
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
    app        fyne.App
    window     fyne.Window
    relay      *UDPRelay
    statusLabel *widget.Label
    logText    *widget.Entry
    startBtn   *widget.Button
    stopBtn    *widget.Button
}

// NewUDPRelayApp 创建新的GUI应用程序
func NewUDPRelayApp() *UDPRelayApp {
    myApp := app.New()
    window := myApp.NewWindow("UDP 中继服务器")
    window.Resize(fyne.NewSize(600, 500))

    return &UDPRelayApp{
        app:    myApp,
        window: window,
    }
}

// createUI 创建用户界面
func (ua *UDPRelayApp) createUI() {
    // 创建输入字段
    listenPortEntry := widget.NewEntry()
    listenPortEntry.SetText("8080")
    listenPortEntry.SetPlaceHolder("监听端口")

    targetHostEntry := widget.NewEntry()
    targetHostEntry.SetText("127.0.0.1")
    targetHostEntry.SetPlaceHolder("目标主机")

    targetPortEntry := widget.NewEntry()
    targetPortEntry.SetText("8081")
    targetPortEntry.SetPlaceHolder("目标端口")

    // 状态标签
    ua.statusLabel = widget.NewLabel("状态: 未启动")
    ua.statusLabel.Alignment = fyne.TextAlignCenter

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
    logScroll.SetMinSize(fyne.NewSize(580, 300))

    content := container.NewVBox(
        widget.NewLabel("UDP 中继服务器配置"),
        inputForm,
        buttonRow,
        statusBox,
        widget.NewSeparator(),
        widget.NewLabel("运行日志"),
        logScroll,
    )

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

func main() {
    relayApp := NewUDPRelayApp()
    relayApp.Run()
}