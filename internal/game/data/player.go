package data

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

// 新建玩家
func NewPlayer() *Player {
	return &Player{
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
	}
}
