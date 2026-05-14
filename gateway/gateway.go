package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

type GenerateFunc func(opts *GenerateOptions) (binaryPath string, err error)

type GenerateOptions struct {
	Name             string
	SpecFile         string
	SpecContent      string
	ServerURL        string
	XCliMode         string
	AllowListFile    string
	DisallowListFile string
	Platforms        []string
	OutputDir        string
}

type RegistrationRequest struct {
	Name             string `json:"name"`
	SpecFile         string `json:"specFile"`
	ServerURL        string `json:"serverUrl"`
	XCliMode         string `json:"xcliMode"`
	AllowListFile    string `json:"allowListFile"`
	DisallowListFile string `json:"disallowListFile"`
	ServMode         string `json:"servMode"`
	Transport        string `json:"transport"`
	GenerateCli      bool   `json:"generateCli"`
	Platform         string `json:"platform"`
}

type RegistrationResponse struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Status         string `json:"status"`
	MCPUrl         string `json:"mcpUrl,omitempty"`
	SSEUrl         string `json:"sseUrl,omitempty"`
	CLIDownloadUrl string `json:"cliDownloadUrl,omitempty"`
}

type Gateway struct {
	storage   *Storage
	processes *ProcessManager
	generate  GenerateFunc
	config    *GatewayConfig
	server    *http.Server
}

type GatewayConfig struct {
	Host     string
	Port     int
	DataDir  string
	DBDriver string
	DBDSN    string
}

var routePattern = regexp.MustCompile(`^/([a-zA-Z0-9_-]+-[a-f0-9-]+)/(mcp|sse)(/.*)?$`)

func NewGateway(config *GatewayConfig, generateFn GenerateFunc) (*Gateway, error) {
	if config.DataDir == "" {
		config.DataDir = "./gateway-data"
	}
	if config.DBDriver == "" {
		config.DBDriver = "sqlite3"
	}
	if config.DBDSN == "" {
		os.MkdirAll(config.DataDir, 0755)
		config.DBDSN = filepath.Join(config.DataDir, "gateway.db")
	}
	if config.Port == 0 {
		config.Port = 9090
	}

	storage, err := NewStorage(config.DBDriver, config.DBDSN)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize storage: %w", err)
	}

	return &Gateway{
		storage:   storage,
		processes: NewProcessManager(),
		generate:  generateFn,
		config:    config,
	}, nil
}

func (g *Gateway) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/register", g.handleRegister)
	mux.HandleFunc("/api/v1/services", g.handleListServices)
	mux.HandleFunc("/api/v1/services/", g.handleGetService)
	mux.HandleFunc("/api/v1/download/", g.handleDownload)
	mux.HandleFunc("/", g.handleProxy)

	addr := fmt.Sprintf("%s:%d", g.config.Host, g.config.Port)
	g.server = &http.Server{Addr: addr, Handler: mux}

	log.Printf("[gateway] Starting MCP Gateway on %s", addr)
	log.Printf("[gateway] Data directory: %s", g.config.DataDir)

	if err := g.recoverServices(); err != nil {
		log.Printf("[gateway] Warning: failed to recover services: %v", err)
	}

	return g.server.ListenAndServe()
}

func (g *Gateway) Stop() {
	g.processes.StopAll()
	g.storage.Close()
	if g.server != nil {
		g.server.Close()
	}
}

func (g *Gateway) recoverServices() error {
	services, err := g.storage.ListServices()
	if err != nil {
		return err
	}

	for _, svc := range services {
		if svc.Status != "running" {
			continue
		}

		binaryPath := filepath.Join(svc.CLIBinaryDir, svc.Name)
		if _, err := os.Stat(binaryPath); err != nil {
			log.Printf("[gateway] Binary not found for service %s, marking as stopped", svc.ID)
			g.storage.UpdateServiceStatus(svc.ID, "stopped")
			continue
		}

		if err := g.processes.Start(svc.ID, binaryPath, svc.Transport, svc.Port); err != nil {
			log.Printf("[gateway] Failed to recover service %s: %v", svc.ID, err)
			g.storage.UpdateServiceStatus(svc.ID, "error")
		} else {
			log.Printf("[gateway] Recovered service %s on port %d", svc.ID, svc.Port)
		}
	}

	return nil
}

func (g *Gateway) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req RegistrationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
		return
	}

	if req.Name == "" || req.SpecFile == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name and specFile are required"})
		return
	}

	if req.ServMode != "port" && req.ServMode != "route" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "servMode must be 'port' or 'route'"})
		return
	}

	if req.Transport != "streamable-http" && req.Transport != "sse" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "transport must be 'streamable-http' or 'sse'"})
		return
	}

	id := uuid.New().String()
	routePath := fmt.Sprintf("%s-%s", req.Name, id[:8])

	specContent, err := readSpecFile(req.SpecFile)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("Failed to read spec file: %v", err)})
		return
	}

	serviceDir := filepath.Join(g.config.DataDir, routePath)
	if err := os.MkdirAll(serviceDir, 0755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("Failed to create service directory: %v", err)})
		return
	}

	localSpecPath := filepath.Join(serviceDir, filepath.Base(req.SpecFile))
	if err := os.WriteFile(localSpecPath, []byte(specContent), 0644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("Failed to copy spec file: %v", err)})
		return
	}

	platforms := parsePlatforms(req.Platform)

	record := &ServiceRecord{
		ID:               id,
		Name:             req.Name,
		SpecFile:         localSpecPath,
		SpecContent:      specContent,
		ServerURL:        req.ServerURL,
		XCliMode:         req.XCliMode,
		AllowListFile:    req.AllowListFile,
		DisallowListFile: req.DisallowListFile,
		ServMode:         req.ServMode,
		Transport:        req.Transport,
		GenerateCli:      req.GenerateCli,
		Platform:         req.Platform,
		Status:           "generating",
		RoutePath:        routePath,
		CLIBinaryDir:     serviceDir,
	}

	if err := g.storage.CreateService(record); err != nil {
		os.RemoveAll(serviceDir)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("Failed to create service record: %v", err)})
		return
	}

	go g.provisionService(record, platforms, localSpecPath)

	host := g.getExternalHost(r)
	mcpUrl := ""
	sseUrl := ""

	if req.ServMode == "route" {
		mcpUrl = fmt.Sprintf("http://%s/%s/mcp", host, routePath)
		sseUrl = fmt.Sprintf("http://%s/%s/sse", host, routePath)
	}

	writeJSON(w, http.StatusAccepted, RegistrationResponse{
		ID:     id,
		Name:   req.Name,
		Status: "generating",
		MCPUrl: mcpUrl,
		SSEUrl: sseUrl,
	})
}

func (g *Gateway) provisionService(record *ServiceRecord, platforms []string, localSpecPath string) {
	genOpts := &GenerateOptions{
		Name:             record.Name,
		SpecFile:         localSpecPath,
		SpecContent:      record.SpecContent,
		ServerURL:        record.ServerURL,
		XCliMode:         record.XCliMode,
		AllowListFile:    record.AllowListFile,
		DisallowListFile: record.DisallowListFile,
		Platforms:        platforms,
		OutputDir:        record.CLIBinaryDir,
	}

	binaryPath, err := g.generate(genOpts)
	if err != nil {
		log.Printf("[gateway] Failed to generate CLI for service %s: %v", record.ID, err)
		g.storage.UpdateServiceStatus(record.ID, "error")
		return
	}

	mcpPort, err := getFreePort()
	if err != nil {
		log.Printf("[gateway] Failed to allocate port for service %s: %v", record.ID, err)
		g.storage.UpdateServiceStatus(record.ID, "error")
		return
	}

	if err := g.processes.Start(record.ID, binaryPath, record.Transport, mcpPort); err != nil {
		log.Printf("[gateway] Failed to start MCP server for service %s: %v", record.ID, err)
		g.storage.UpdateServiceStatus(record.ID, "error")
		return
	}

	host := ""
	mcpUrl := ""
	sseUrl := ""
	cliDownloadUrl := ""

	if record.ServMode == "port" {
		host = getLocalIP()
		mcpUrl = fmt.Sprintf("http://%s:%d/mcp", host, mcpPort)
		sseUrl = fmt.Sprintf("http://%s:%d/sse", host, mcpPort)
	} else {
		host = g.getExternalHostFromConfig()
		mcpUrl = fmt.Sprintf("http://%s/%s/mcp", host, record.RoutePath)
		sseUrl = fmt.Sprintf("http://%s/%s/sse", host, record.RoutePath)
	}

	if record.GenerateCli && len(platforms) > 0 {
		p := platforms[0]
		cliDownloadUrl = fmt.Sprintf("http://%s/api/v1/download/%s/%s", g.getExternalHostFromConfig(), record.ID, p)
	}

	pid := 0
	if info, ok := g.processes.processes[record.ID]; ok && info.Cmd.Process != nil {
		pid = info.Cmd.Process.Pid
	}

	g.storage.UpdateServiceRuntime(record.ID, mcpPort, record.RoutePath, mcpUrl, sseUrl, cliDownloadUrl, record.CLIBinaryDir, pid)

	log.Printf("[gateway] Service %s is running on port %d (mode=%s, transport=%s)", record.ID, mcpPort, record.ServMode, record.Transport)
}

func (g *Gateway) handleListServices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	services, err := g.storage.ListServices()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, services)
}

func (g *Gateway) handleGetService(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/api/v1/services/")

	if r.Method == http.MethodDelete {
		g.processes.Stop(id)
		svc, _ := g.storage.GetService(id)
		if svc != nil && svc.CLIBinaryDir != "" {
			os.RemoveAll(svc.CLIBinaryDir)
		}
		g.storage.DeleteService(id)
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
		return
	}

	svc, err := g.storage.GetService(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if svc == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Service not found"})
		return
	}

	writeJSON(w, http.StatusOK, svc)
}

func (g *Gateway) handleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/download/"), "/")
	if len(parts) < 3 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid download path, expected /api/v1/download/{id}/{os}/{arch}"})
		return
	}

	id := parts[0]
	platform := parts[1] + "/" + parts[2]

	svc, err := g.storage.GetService(id)
	if err != nil || svc == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Service not found"})
		return
	}

	osArch := strings.Split(platform, "/")
	if len(osArch) != 2 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid platform format, expected os/arch"})
		return
	}

	binaryName := svc.Name + "-" + osArch[0] + "-" + osArch[1]
	binaryPath := filepath.Join(svc.CLIBinaryDir, binaryName)

	if _, err := os.Stat(binaryPath); err != nil {
		binaryPath = filepath.Join(svc.CLIBinaryDir, svc.Name)
		if _, err := os.Stat(binaryPath); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "CLI binary not found"})
			return
		}
		binaryName = svc.Name
	}

	if osArch[0] == "windows" && !strings.HasSuffix(binaryName, ".exe") {
		exePath := binaryPath + ".exe"
		if _, err := os.Stat(exePath); err == nil {
			binaryPath = exePath
			binaryName = binaryName + ".exe"
		}
	}

	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", binaryName))
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, binaryPath)
}

func (g *Gateway) handleProxy(w http.ResponseWriter, r *http.Request) {
	matches := routePattern.FindStringSubmatch(r.URL.Path)
	if matches == nil {
		http.NotFound(w, r)
		return
	}

	routePath := matches[1]
	endpoint := matches[2]
	remaining := ""
	if len(matches) > 3 {
		remaining = matches[3]
	}

	var svc *ServiceRecord
	services, _ := g.storage.ListServices()
	for _, s := range services {
		if s.RoutePath == routePath {
			svc = s
			break
		}
	}

	if svc == nil || svc.Status != "running" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Service not found or not running"})
		return
	}

	port, ok := g.processes.GetPort(svc.ID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Service process not found"})
		return
	}

	targetPath := "/" + endpoint
	if remaining != "" {
		targetPath += remaining
	}

	targetURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	proxy := &reverseProxy{
		target:  targetURL,
		path:    targetPath,
		service: svc.ID,
	}
	proxy.ServeHTTP(w, r)
}

func (g *Gateway) getExternalHost(r *http.Request) string {
	if g.config.Host != "" && g.config.Host != "0.0.0.0" {
		return fmt.Sprintf("%s:%d", g.config.Host, g.config.Port)
	}
	host := r.Host
	if host == "" {
		host = fmt.Sprintf("localhost:%d", g.config.Port)
	}
	return host
}

func (g *Gateway) getExternalHostFromConfig() string {
	host := g.config.Host
	if host == "" || host == "0.0.0.0" {
		host = "localhost"
	}
	return fmt.Sprintf("%s:%d", host, g.config.Port)
}

type reverseProxy struct {
	target  string
	path    string
	service string
}

func (p *reverseProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	targetURL := p.target + p.path + "?" + r.URL.RawQuery

	outReq, err := http.NewRequest(r.Method, targetURL, r.Body)
	if err != nil {
		http.Error(w, "Proxy error", http.StatusInternalServerError)
		return
	}

	outReq.Header = r.Header.Clone()
	outReq.Host = r.Host

	resp, err := http.DefaultClient.Do(outReq)
	if err != nil {
		http.Error(w, fmt.Sprintf("Proxy error: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}

	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func readSpecFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func parsePlatforms(platform string) []string {
	if platform == "" {
		return []string{currentPlatform()}
	}
	parts := strings.Split(platform, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	if len(result) == 0 {
		return []string{currentPlatform()}
	}
	return result
}

func currentPlatform() string {
	return fmt.Sprintf("%s/%s", getGOOS(), getGOARCH())
}

func getLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
			return ipnet.IP.String()
		}
	}
	return "127.0.0.1"
}

func getFreePort() (int, error) {
	addr, err := net.ResolveTCPAddr("tcp", "localhost:0")
	if err != nil {
		return 0, err
	}
	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

var (
	goos   string
	goarch string
)

func getGOOS() string {
	if goos != "" {
		return goos
	}
	return "linux"
}

func getGOARCH() string {
	if goarch != "" {
		return goarch
	}
	return "amd64"
}
