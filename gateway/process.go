package gateway

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"
)

type ProcessInfo struct {
	ID     string
	Cmd    *exec.Cmd
	Port   int
	Cancel context.CancelFunc
}

type ProcessManager struct {
	processes map[string]*ProcessInfo
	mu        sync.RWMutex
}

func NewProcessManager() *ProcessManager {
	return &ProcessManager{
		processes: make(map[string]*ProcessInfo),
	}
}

func (pm *ProcessManager) Start(id, binaryPath string, transport string, port int) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if _, exists := pm.processes[id]; exists {
		return fmt.Errorf("process already running for service %s", id)
	}

	ctx, cancel := context.WithCancel(context.Background())

	args := []string{"mcp", "serve", "-t", transport, "-p", strconv.Itoa(port)}
	cmd := exec.CommandContext(ctx, binaryPath, args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("failed to start process: %w", err)
	}

	pm.processes[id] = &ProcessInfo{
		ID:     id,
		Cmd:    cmd,
		Port:   port,
		Cancel: cancel,
	}

	go func() {
		cmd.Wait()
		pm.mu.Lock()
		delete(pm.processes, id)
		pm.mu.Unlock()
	}()

	return nil
}

func (pm *ProcessManager) Stop(id string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	info, exists := pm.processes[id]
	if !exists {
		return nil
	}

	info.Cancel()
	if info.Cmd.Process != nil {
		info.Cmd.Process.Signal(os.Interrupt)
	}

	delete(pm.processes, id)
	return nil
}

func (pm *ProcessManager) StopAll() {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	for id, info := range pm.processes {
		info.Cancel()
		if info.Cmd.Process != nil {
			info.Cmd.Process.Signal(os.Interrupt)
		}
		delete(pm.processes, id)
	}
}

func (pm *ProcessManager) GetPort(id string) (int, bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	info, exists := pm.processes[id]
	if !exists {
		return 0, false
	}
	return info.Port, true
}

func (pm *ProcessManager) IsRunning(id string) bool {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	_, exists := pm.processes[id]
	return exists
}
