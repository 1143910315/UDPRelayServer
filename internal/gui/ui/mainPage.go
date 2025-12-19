package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"
)

// 主页面
type MainPage struct {
	Window               fyne.Window
	Tabs                 *container.AppTabs
	ListenPortEntry      *widget.Entry
	TargetPortEntry      *widget.Entry
	TargetHostEntry      *widget.Entry
	HistoryAddressSelect *widget.Select
	StartBtn             *widget.Button
	StopBtn              *widget.Button
	ClearBtn             *widget.Button
	ConnectBtn           *widget.Button
	DisconnectBtn        *widget.Button
	ClearBtn1            *widget.Button
	PlayerTable          *widget.Table
	LogText              *widget.Entry
}

// 创建主页面
func NewMainPage(app fyne.App) *MainPage {
	mainPage := &MainPage{
		Window:               app.NewWindow("UDP 中继服务器"),
		Tabs:                 container.NewAppTabs(),
		ListenPortEntry:      widget.NewEntry(),
		TargetPortEntry:      widget.NewEntry(),
		TargetHostEntry:      widget.NewEntry(),
		HistoryAddressSelect: widget.NewSelect([]string{}, nil),
		StartBtn:             widget.NewButton("启动服务器", nil),
		StopBtn:              widget.NewButton("停止服务器", nil),
		ClearBtn:             widget.NewButton("清空日志", nil),
		ConnectBtn:           widget.NewButton("连接服务器", nil),
		DisconnectBtn:        widget.NewButton("断开服务器", nil),
		ClearBtn1:            widget.NewButton("清空日志", nil),
		PlayerTable:          widget.NewTable(nil, nil, nil),
		LogText:              widget.NewMultiLineEntry(),
	}

	mainPage.ListenPortEntry.SetPlaceHolder("监听端口")
	mainPage.TargetPortEntry.SetPlaceHolder("游戏端口")
	mainPage.TargetHostEntry.SetPlaceHolder("目标主机")

	mainPage.LogText.Disable()

	// 创建玩家列表表格
	playerTableScroll := container.NewScroll(mainPage.PlayerTable)
	playerTableScroll.SetMinSize(fyne.NewSize(550, 150))

	// 主机模式的内容
	hostContent := container.NewVBox(
		container.NewBorder(nil, nil, widget.NewLabel("监听端口"), nil, mainPage.ListenPortEntry),
		container.NewBorder(nil, nil, widget.NewLabel("游戏端口"), nil, mainPage.TargetPortEntry),
		container.NewHBox(mainPage.StartBtn, mainPage.StopBtn, mainPage.ClearBtn),
	)

	// 客机模式的内容
	clientContent := container.NewVBox(
		container.NewBorder(nil, nil, widget.NewLabel("目标主机"), nil, mainPage.TargetHostEntry),
		container.NewBorder(nil, nil, widget.NewLabel("历史主机"), nil, mainPage.HistoryAddressSelect),
		layout.NewSpacer(),
		container.NewHBox(mainPage.ConnectBtn, mainPage.DisconnectBtn, mainPage.ClearBtn1),
	)

	mainPage.Tabs.Items = []*container.TabItem{
		container.NewTabItem("作为主机", hostContent),
		container.NewTabItem("作为客机", clientContent),
	}

	logScroll := container.NewScroll(mainPage.LogText)
	logScroll.SetMinSize(fyne.NewSize(550, 150))

	topContent := container.NewVBox(
		mainPage.Tabs,
		widget.NewSeparator(),
		widget.NewLabel("玩家列表"),
		playerTableScroll,
		widget.NewSeparator(),
		widget.NewLabel("运行日志"),
	)

	content := container.NewBorder(topContent, nil, nil, nil, logScroll)

	mainPage.Window.SetContent(content)
	return mainPage
}

// 显示窗口
func (mp *MainPage) Show() {
	mp.Window.ShowAndRun()
}

// 设置主机模式
func (mp *MainPage) SetHostStatus() {
	mp.ListenPortEntry.Disable()
	mp.TargetPortEntry.Disable()
	mp.TargetHostEntry.Disable()
	mp.HistoryAddressSelect.Disable()
	mp.StartBtn.Disable()
	mp.StopBtn.Enable()
	mp.ConnectBtn.Disable()
	mp.DisconnectBtn.Disable()
}

// 设置客机模式
func (mp *MainPage) SetClientStatus() {
	mp.ListenPortEntry.Disable()
	mp.TargetPortEntry.Disable()
	mp.TargetHostEntry.Disable()
	mp.HistoryAddressSelect.Disable()
	mp.StartBtn.Disable()
	mp.StopBtn.Disable()
	mp.ConnectBtn.Disable()
	mp.DisconnectBtn.Enable()
}

// 设置空闲状态
func (mp *MainPage) SetIdleStatus() {
	mp.ListenPortEntry.Enable()
	mp.TargetPortEntry.Enable()
	mp.TargetHostEntry.Enable()
	mp.HistoryAddressSelect.Enable()
	mp.StartBtn.Enable()
	mp.StopBtn.Disable()
	mp.ConnectBtn.Enable()
	mp.DisconnectBtn.Disable()
}

// 更新玩家表格显示
func (mp *MainPage) RefreshPlayerTable() {
	mp.PlayerTable.Refresh()
}

// 更新玩家表格显示
func (mp *MainPage) RefreshPlayerTableItem(row int, col int) {
	mp.PlayerTable.RefreshItem(widget.TableCellID{Row: row, Col: col})
}
