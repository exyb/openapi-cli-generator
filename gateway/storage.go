package gateway

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type ServiceRecord struct {
	ID               string
	Name             string
	SpecFile         string
	SpecContent      string
	ServerURL        string
	XCliMode         string
	AllowListFile    string
	DisallowListFile string
	ServMode         string
	Transport        string
	GenerateCli      bool
	Platform         string
	Status           string
	Port             int
	RoutePath        string
	CLIBinaryDir     string
	ProcessPID       int
	MCPUrl           string
	SSEUrl           string
	CLIDownloadUrl   string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type Storage struct {
	db *sql.DB
}

var schemaSQL = `
CREATE TABLE IF NOT EXISTS services (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	spec_file TEXT NOT NULL,
	spec_content TEXT,
	server_url TEXT,
	xcli_mode TEXT,
	allow_list_file TEXT,
	disallow_list_file TEXT,
	serv_mode TEXT NOT NULL,
	transport TEXT NOT NULL DEFAULT 'streamable-http',
	generate_cli INTEGER DEFAULT 0,
	platform TEXT,
	status TEXT DEFAULT 'pending',
	port INTEGER DEFAULT 0,
	route_path TEXT,
	cli_binary_dir TEXT,
	process_pid INTEGER DEFAULT 0,
	mcp_url TEXT,
	sse_url TEXT,
	cli_download_url TEXT,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
`

func NewStorage(driver, dsn string) (*Storage, error) {
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	db.SetMaxOpenConns(1)

	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create schema: %w", err)
	}

	return &Storage{db: db}, nil
}

func (s *Storage) Close() error {
	return s.db.Close()
}

func (s *Storage) CreateService(r *ServiceRecord) error {
	_, err := s.db.Exec(`
		INSERT INTO services (id, name, spec_file, spec_content, server_url, xcli_mode,
			allow_list_file, disallow_list_file, serv_mode, transport, generate_cli,
			platform, status, port, route_path, cli_binary_dir, process_pid,
			mcp_url, sse_url, cli_download_url)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Name, r.SpecFile, r.SpecContent, r.ServerURL, r.XCliMode,
		r.AllowListFile, r.DisallowListFile, r.ServMode, r.Transport, r.GenerateCli,
		r.Platform, r.Status, r.Port, r.RoutePath, r.CLIBinaryDir, r.ProcessPID,
		r.MCPUrl, r.SSEUrl, r.CLIDownloadUrl)
	return err
}

func (s *Storage) GetService(id string) (*ServiceRecord, error) {
	row := s.db.QueryRow(`
		SELECT id, name, spec_file, spec_content, server_url, xcli_mode,
			allow_list_file, disallow_list_file, serv_mode, transport, generate_cli,
			platform, status, port, route_path, cli_binary_dir, process_pid,
			mcp_url, sse_url, cli_download_url, created_at, updated_at
		FROM services WHERE id = ?`, id)

	var r ServiceRecord
	var generateCli int
	var createdAt, updatedAt string

	err := row.Scan(&r.ID, &r.Name, &r.SpecFile, &r.SpecContent, &r.ServerURL, &r.XCliMode,
		&r.AllowListFile, &r.DisallowListFile, &r.ServMode, &r.Transport, &generateCli,
		&r.Platform, &r.Status, &r.Port, &r.RoutePath, &r.CLIBinaryDir, &r.ProcessPID,
		&r.MCPUrl, &r.SSEUrl, &r.CLIDownloadUrl, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	r.GenerateCli = generateCli == 1
	r.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
	r.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updatedAt)

	return &r, nil
}

func (s *Storage) ListServices() ([]*ServiceRecord, error) {
	rows, err := s.db.Query(`
		SELECT id, name, spec_file, spec_content, server_url, xcli_mode,
			allow_list_file, disallow_list_file, serv_mode, transport, generate_cli,
			platform, status, port, route_path, cli_binary_dir, process_pid,
			mcp_url, sse_url, cli_download_url, created_at, updated_at
		FROM services ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []*ServiceRecord
	for rows.Next() {
		var r ServiceRecord
		var generateCli int
		var createdAt, updatedAt string

		err := rows.Scan(&r.ID, &r.Name, &r.SpecFile, &r.SpecContent, &r.ServerURL, &r.XCliMode,
			&r.AllowListFile, &r.DisallowListFile, &r.ServMode, &r.Transport, &generateCli,
			&r.Platform, &r.Status, &r.Port, &r.RoutePath, &r.CLIBinaryDir, &r.ProcessPID,
			&r.MCPUrl, &r.SSEUrl, &r.CLIDownloadUrl, &createdAt, &updatedAt)
		if err != nil {
			return nil, err
		}

		r.GenerateCli = generateCli == 1
		r.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
		r.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updatedAt)

		results = append(results, &r)
	}

	return results, nil
}

func (s *Storage) UpdateServiceStatus(id, status string) error {
	_, err := s.db.Exec(`
		UPDATE services SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		status, id)
	return err
}

func (s *Storage) UpdateServiceRuntime(id string, port int, routePath, mcpUrl, sseUrl, cliDownloadUrl, cliBinaryDir string, pid int) error {
	_, err := s.db.Exec(`
		UPDATE services SET port = ?, route_path = ?, mcp_url = ?, sse_url = ?,
			cli_download_url = ?, cli_binary_dir = ?, process_pid = ?,
			status = 'running', updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		port, routePath, mcpUrl, sseUrl, cliDownloadUrl, cliBinaryDir, pid, id)
	return err
}

func (s *Storage) DeleteService(id string) error {
	_, err := s.db.Exec(`DELETE FROM services WHERE id = ?`, id)
	return err
}
