package game

import (
	"sync"

	"github.com/1143910315/UDPRelayServer/internal/config"
)

type PlayerManager struct {
	configManager *config.ConfigManager
	Players       []*Player
	PlayersMutex  sync.RWMutex
}

// 创建一个新的PlayerManager
func NewPlayerManager(configManager *config.ConfigManager) *PlayerManager {
	return &PlayerManager{
		configManager: configManager,
		Players:       make([]*Player, 0),
	}
}
