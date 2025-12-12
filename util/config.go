package util

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/bestnite/sub2sing-box/constant"
	"github.com/bestnite/sub2sing-box/model"
)

var (
	configCache    *model.ConvertRequest
	configMtime    time.Time
	configFilePath = constant.ConfigFile
	cacheMutex     sync.RWMutex
)

func getDefaultConfig() *model.ConvertRequest {
	return &model.ConvertRequest{
		Group:    true,
		SortKey:  "tag",
		SortType: "asc",
	}
}

func LoadConfigIfNeeded() (*model.ConvertRequest, error) {
	fileInfo, err := os.Stat(configFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			cacheMutex.Lock()
			defer cacheMutex.Unlock()

			if configCache == nil {
				defaultCfg := getDefaultConfig()
				configCache = defaultCfg
				configMtime = time.Unix(0, 0)
			}
			return deepCopyConfig(configCache), nil
		}
		return nil, err
	}

	cacheMutex.RLock()
	currentMtime := configMtime
	cacheMutex.RUnlock()

	if !fileInfo.ModTime().After(currentMtime) {
		cacheMutex.RLock()
		defer cacheMutex.RUnlock()
		return deepCopyConfig(configCache), nil
	}

	cacheMutex.Lock()
	defer cacheMutex.Unlock()

	if !fileInfo.ModTime().After(configMtime) {
		return deepCopyConfig(configCache), nil
	}

	rawData, err := os.ReadFile(configFilePath)
	if err != nil {
		return nil, err
	}

	var data model.ConvertRequest
	if err := json.Unmarshal(rawData, &data); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	if data.Rename == nil {
		data.Rename = make(map[string]string)
	}
	if data.Proxies == nil {
		data.Proxies = []string{}
	}
	if data.Subscriptions == nil {
		data.Subscriptions = []string{}
	}

	configCache = &data
	configMtime = fileInfo.ModTime()

	return deepCopyConfig(configCache), nil
}

func deepCopyConfig(src *model.ConvertRequest) *model.ConvertRequest {
	if src == nil {
		return nil
	}

	dst := &model.ConvertRequest{
		Subscriptions: make([]string, len(src.Subscriptions)),
		Proxies:       make([]string, len(src.Proxies)),
		Template:      src.Template,
		Delete:        src.Delete,
		Rename:        make(map[string]string, len(src.Rename)),
		Group:         src.Group,
		GroupType:     src.GroupType,
		SortKey:       src.SortKey,
		SortType:      src.SortType,
		Output:        src.Output,
		GroupRules:    src.GroupRules,
		Version:       src.Version,
	}

	copy(dst.Subscriptions, src.Subscriptions)
	copy(dst.Proxies, src.Proxies)

	for k, v := range src.Rename {
		dst.Rename[k] = v
	}

	return dst
}
