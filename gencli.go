package main

import (
	"fmt"
	"go/format"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"

	"github.com/danielgtaylor/openapi-toolkit/gateway"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/spf13/cobra"
)

type GenCLIOptions struct {
	Name             string
	SpecFile         string
	ServerURL        string
	XCliMode         string
	AllowListFile    string
	DisallowListFile string
	Platform         string
	Install          bool
}

func runGenCLI(cmd *cobra.Command, args []string) {
	name, _ := cmd.Flags().GetString("name")
	specFile, _ := cmd.Flags().GetString("spec-file")
	serverURL, _ := cmd.Flags().GetString("server-url")
	xcliMode, _ := cmd.Flags().GetString("xcli-mode")
	allowListFile, _ := cmd.Flags().GetString("allow-list-file")
	disallowListFile, _ := cmd.Flags().GetString("disallow-list-file")
	platform, _ := cmd.Flags().GetString("platform")
	install, _ := cmd.Flags().GetBool("install")

	if name == "" {
		log.Fatal("--name is required")
	}
	if specFile == "" {
		log.Fatal("--spec-file is required")
	}

	opts := &GenCLIOptions{
		Name:             name,
		SpecFile:         specFile,
		ServerURL:        serverURL,
		XCliMode:         xcliMode,
		AllowListFile:    allowListFile,
		DisallowListFile: disallowListFile,
		Platform:         platform,
		Install:          install,
	}

	platforms := parsePlatformList(opts.Platform)
	outputDir, _ := os.Getwd()

	genOpts := &gateway.GenerateOptions{
		Name:             opts.Name,
		SpecFile:         opts.SpecFile,
		ServerURL:        opts.ServerURL,
		XCliMode:         opts.XCliMode,
		AllowListFile:    opts.AllowListFile,
		DisallowListFile: opts.DisallowListFile,
		Platforms:        platforms,
		OutputDir:        outputDir,
	}

	binaryPath, err := generateAndBuild(genOpts)
	if err != nil {
		log.Fatalf("Failed to generate CLI: %v", err)
	}

	fmt.Printf("Successfully generated CLI: %s\n", binaryPath)
}

func generateAndBuild(opts *gateway.GenerateOptions) (string, error) {
	tempDir, err := ioutil.TempDir("", "openapi-toolkit-gen-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp directory: %w", err)
	}

	keepSource := os.Getenv("OPENAPI_TOOLKIT_KEEP_SOURCE") != ""
	if !keepSource {
		defer os.RemoveAll(tempDir)
	} else {
		log.Printf("[gen-cli] Keeping source at: %s", tempDir)
	}

	mainGo, commandsGo, err := generateSourceCode(opts)
	if err != nil {
		return "", fmt.Errorf("failed to generate source code: %w", err)
	}

	if err := ioutil.WriteFile(filepath.Join(tempDir, "main.go"), mainGo, 0600); err != nil {
		return "", fmt.Errorf("failed to write main.go: %w", err)
	}

	if err := ioutil.WriteFile(filepath.Join(tempDir, "openapi.go"), commandsGo, 0600); err != nil {
		return "", fmt.Errorf("failed to write openapi.go: %w", err)
	}

	toolkitDir, err := findToolkitDir()
	if err != nil {
		return "", fmt.Errorf("failed to find toolkit directory: %w", err)
	}

	goModContent := fmt.Sprintf(`module %s

go 1.25

require github.com/danielgtaylor/openapi-toolkit v0.0.0

replace github.com/danielgtaylor/openapi-toolkit => %s
`, opts.Name, toolkitDir)

	if err := ioutil.WriteFile(filepath.Join(tempDir, "go.mod"), []byte(goModContent), 0600); err != nil {
		return "", fmt.Errorf("failed to write go.mod: %w", err)
	}

	tidyCmd := exec.Command("go", "mod", "tidy")
	tidyCmd.Dir = tempDir
	tidyCmd.Stdout = os.Stderr
	tidyCmd.Stderr = os.Stderr
	if err := tidyCmd.Run(); err != nil {
		return "", fmt.Errorf("go mod tidy failed: %w", err)
	}

	var binaryPaths []string
	for _, platform := range opts.Platforms {
		parts := strings.Split(platform, "/")
		if len(parts) != 2 {
			return "", fmt.Errorf("invalid platform format: %s (expected os/arch)", platform)
		}
		goos, goarch := parts[0], parts[1]

		outputName := opts.Name
		if len(opts.Platforms) > 1 {
			outputName = fmt.Sprintf("%s-%s-%s", opts.Name, goos, goarch)
		}
		if goos == "windows" {
			outputName += ".exe"
		}

		outputPath := filepath.Join(opts.OutputDir, outputName)
		if info, err := os.Stat(outputPath); err == nil && info.IsDir() {
			outputPath = filepath.Join(outputPath, outputName)
		}

		buildCmd := exec.Command("go", "build", "-o", outputPath, ".")
		buildCmd.Dir = tempDir
		buildCmd.Env = append(os.Environ(),
			"GOOS="+goos,
			"GOARCH="+goarch,
			"CGO_ENABLED=0",
		)
		buildCmd.Stdout = os.Stderr
		buildCmd.Stderr = os.Stderr
		if err := buildCmd.Run(); err != nil {
			return "", fmt.Errorf("go build failed for %s: %w", platform, err)
		}

		binaryPaths = append(binaryPaths, outputPath)
		fmt.Printf("Built: %s\n", outputPath)
	}

	if len(binaryPaths) > 0 {
		return binaryPaths[0], nil
	}
	return "", fmt.Errorf("no binaries were built")
}

func generateSourceCode(opts *gateway.GenerateOptions) (mainGo, commandsGo []byte, err error) {
	specData, err := ioutil.ReadFile(opts.SpecFile)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read spec file: %w", err)
	}

	loader := openapi3.NewSwaggerLoader()
	swagger, err := loader.LoadSwaggerFromData(specData)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load OpenAPI document: %w", err)
	}

	if opts.ServerURL != "" {
		swagger.Servers = openapi3.Servers{
			&openapi3.Server{
				URL: opts.ServerURL,
			},
		}
	}

	enableXCliDravh := opts.XCliMode == "dravh"

	var pathFilter *PathFilter
	if opts.AllowListFile != "" {
		pathFilter = loadPathFilter(opts.AllowListFile, true)
	} else if opts.DisallowListFile != "" {
		pathFilter = loadPathFilter(opts.DisallowListFile, false)
	}

	templateData := ProcessAPI("openapi", swagger, specData, enableXCliDravh, pathFilter)

	mainGo, err = generateMainGo(opts.Name)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate main.go: %w", err)
	}

	mainGoStr := string(mainGo)
	mainGoStr = strings.Replace(mainGoStr, "// TODO: Add register commands here.", "openapiRegister(true)", 1)

	if opts.ServerURL != "" {
		setDefaultLine := fmt.Sprintf("\tviper.SetDefault(\"server\", \"%s\")", opts.ServerURL)
		mainGoStr = strings.Replace(mainGoStr, "cli.Root.Execute()", setDefaultLine+"\n\n\tcli.Root.Execute()", 1)
	}

	mainGo = []byte(mainGoStr)

	funcs := template.FuncMap{
		"escapeStr": escapeString,
		"slug":      slug,
		"title":     strings.Title,
	}

	cmdData, _ := Asset("templates/commands.tmpl")
	tmpl, err := template.New("cli").Funcs(funcs).Parse(string(cmdData))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse commands template: %w", err)
	}

	var sb strings.Builder
	if err := tmpl.Execute(&sb, templateData); err != nil {
		return nil, nil, fmt.Errorf("failed to execute commands template: %w", err)
	}

	commandsGo = []byte(sb.String())

	formatted, errFmt := format.Source(mainGo)
	if errFmt == nil {
		mainGo = formatted
	}

	formatted, errFmt = format.Source(commandsGo)
	if errFmt == nil {
		commandsGo = formatted
	}

	return mainGo, commandsGo, nil
}

func generateMainGo(name string) ([]byte, error) {
	data, _ := Asset("templates/main.tmpl")
	tmpl, err := template.New("cli").Parse(string(data))
	if err != nil {
		return nil, err
	}

	templateData := map[string]string{
		"Name":    name,
		"NameEnv": strings.Replace(strings.ToUpper(name), "-", "_", -1),
	}

	var sb strings.Builder
	if err := tmpl.Execute(&sb, templateData); err != nil {
		return nil, err
	}

	return []byte(sb.String()), nil
}

func findToolkitDir() (string, error) {
	if dir := os.Getenv("OPENAPI_TOOLKIT_DIR"); dir != "" {
		return dir, nil
	}

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("cannot determine toolkit source directory, set OPENAPI_TOOLKIT_DIR env var")
	}

	dir := filepath.Dir(filename)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			content, err := ioutil.ReadFile(filepath.Join(dir, "go.mod"))
			if err == nil && strings.Contains(string(content), "github.com/danielgtaylor/openapi-toolkit") {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return "", fmt.Errorf("cannot find toolkit go.mod, set OPENAPI_TOOLKIT_DIR env var")
}

func parsePlatformList(platform string) []string {
	if platform == "" {
		return []string{fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)}
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
		return []string{fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)}
	}
	return result
}

func gatewayGenerateFunc(opts *gateway.GenerateOptions) (string, error) {
	outputDir := opts.OutputDir
	if outputDir == "" {
		outputDir, _ = os.Getwd()
	}
	opts.OutputDir = outputDir

	if opts.SpecContent != "" && opts.SpecFile != "" {
		specDir := filepath.Dir(opts.SpecFile)
		tempSpec, err := ioutil.TempFile(specDir, "spec-*.yaml")
		if err != nil {
			return "", err
		}
		defer os.Remove(tempSpec.Name())
		if _, err := tempSpec.WriteString(opts.SpecContent); err != nil {
			return "", err
		}
		tempSpec.Close()
		opts.SpecFile = tempSpec.Name()
		opts.SpecContent = ""
	}

	return generateAndBuild(opts)
}

func runMCPGateway(cmd *cobra.Command, args []string) {
	host, _ := cmd.Flags().GetString("host")
	port, _ := cmd.Flags().GetInt("port")
	dataDir, _ := cmd.Flags().GetString("data-dir")
	dbDriver, _ := cmd.Flags().GetString("db-driver")
	dbDSN, _ := cmd.Flags().GetString("db-dsn")

	config := &gateway.GatewayConfig{
		Host:     host,
		Port:     port,
		DataDir:  dataDir,
		DBDriver: dbDriver,
		DBDSN:    dbDSN,
	}

	gw, err := gateway.NewGateway(config, gatewayGenerateFunc)
	if err != nil {
		log.Fatalf("Failed to create gateway: %v", err)
	}
	defer gw.Stop()

	if err := gw.Start(); err != nil {
		log.Fatalf("Gateway error: %v", err)
	}
}

func shortNameFromSpec(specFile string) string {
	shortName := path.Base(specFile)
	shortName = strings.TrimSuffix(shortName, ".yaml")
	shortName = strings.TrimSuffix(shortName, ".json")
	shortName = strings.TrimSuffix(shortName, ".yml")
	if shortName != "openapi" {
		shortName = "openapi"
	}
	return shortName
}
