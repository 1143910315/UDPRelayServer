package config

import (
	"database/sql"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/1143910315/UDPRelayServer/internal/security"
	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
)

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
		publicKey, privateKey, err := security.GenerateKeyPair()
		if err != nil {
			return "", "", "", err
		}

		serviceID, err := uuid.NewUUID()
		if err != nil {
			return "", "", "", err
		}

		_, err = cm.db.Exec("INSERT INTO key_pairs (id, service_id, private_key, public_key) VALUES (?, ?, ?, ?)", id, serviceID.String(), privateKey, publicKey)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint") {
				// service_id 重复，继续重试
				continue
			}
			return "", "", "", err
		}

		return serviceID.String(), privateKey, publicKey, nil
	}
	return "", "", "", errors.New("插入密钥对失败，已达到最大重试次数")
}

// 从数据库获取密钥对
func (cm *ConfigManager) GetKeyPairByID(id int) (string, string, string, error) {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	var serviceID, privateKey, publicKey string
	err := cm.db.QueryRow("SELECT service_id, private_key, public_key FROM key_pairs WHERE id = ?", id).
		Scan(&serviceID, &privateKey, &publicKey)
	if err != nil {
		if err == sql.ErrNoRows {
			// 数据不存在，插入新数据
			return cm.insertKeyPair(id)
		}
		return "", "", "", err
	}
	return serviceID, privateKey, publicKey, nil
}

// 根据serviceID查询认证码和设备id
func (cm *ConfigManager) GetServerByServiceID(serviceID string) (string, string, error) {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	var authCode, deviceID string
	err := cm.db.QueryRow("SELECT auth_code, device_id FROM servers WHERE service_id = ?", serviceID).
		Scan(&authCode, &deviceID)
	if err != nil {
		return "", "", err
	}
	return authCode, deviceID, nil
}

// 插入serviceID、认证码、设备id
func (cm *ConfigManager) InsertServer(serviceID, authCode, deviceID string) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	_, err := cm.db.Exec("INSERT INTO servers (service_id, auth_code, device_id) VALUES (?, ?, ?)", serviceID, authCode, deviceID)
	return err
}

// 设置设备认证信息
func (cm *ConfigManager) SetDeviceAuthentication(deviceID, authenticationID string) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	_, err := cm.db.Exec("INSERT OR REPLACE INTO device_authentication (device_id, authentication_id) VALUES (?, ?)", deviceID, authenticationID)
	return err
}

// 获取设备认证信息
func (cm *ConfigManager) GetDeviceAuthentication(deviceID string) (string, error) {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	var authenticationID string
	err := cm.db.QueryRow("SELECT authentication_id FROM device_authentication WHERE device_id = ?", deviceID).Scan(&authenticationID)
	if err != nil {
		return "", err
	}
	return authenticationID, nil
}

// 设置设备服务备注
func (cm *ConfigManager) SetDeviceServiceRemark(deviceID, serviceID, remark string) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	_, err := cm.db.Exec("INSERT OR REPLACE INTO device_service_remark (device_id, service_id, remark) VALUES (?, ?, ?)", deviceID, serviceID, remark)
	return err
}

// 获取设备服务备注
func (cm *ConfigManager) GetDeviceServiceRemark(deviceID, serviceID string) (string, error) {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	var remark string
	err := cm.db.QueryRow("SELECT remark FROM device_service_remark WHERE device_id = ? AND service_id = ?", deviceID, serviceID).Scan(&remark)
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
