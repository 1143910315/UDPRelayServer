package data

import (
	"sync"
)

type PlayerManager struct {
	Players      []*Player
	PlayersMutex sync.RWMutex
}

// 创建一个新的PlayerManager
func NewPlayerManager() *PlayerManager {
	return &PlayerManager{
		Players: make([]*Player, 0),
	}
}
