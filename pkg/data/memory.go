package data

import (
	"sync"
)

type Memory struct {
	mu   sync.RWMutex
	data map[string]interface{}
}

func (m *Memory) Set(path string, data interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.data[path] = data

	return nil
}

func (m *Memory) Get(path string) (interface{}, error) {
	return m.data[path], nil
}
