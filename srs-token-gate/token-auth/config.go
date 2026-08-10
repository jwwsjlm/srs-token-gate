package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	defaultConfigPath   = "/workspace/config.yaml"
	defaultTemplatePath = "/workspace/config.example.yaml"
	defaultDataPath     = "/workspace/data"
	defaultSRSOutput    = "/generated/token-auth.conf"
	defaultSRSTemplate  = "/workspace/conf/srs/token-auth.conf"
)

type Config struct {
	Version int `yaml:"version"`
	Public  struct {
		Address string `yaml:"address"`
	} `yaml:"public"`
	Admin struct {
		Username     string `yaml:"username"`
		Password     string `yaml:"password"`
		PasswordHash string `yaml:"password_hash"`
	} `yaml:"admin"`
	Session struct {
		CookieSecure bool  `yaml:"cookie_secure"`
		MaxAge       int64 `yaml:"max_age_seconds"`
	} `yaml:"session"`
	SRS struct {
		Candidate      string `yaml:"candidate"`
		HookSecret     string `yaml:"hook_secret"`
		MaxConnections int    `yaml:"max_connections"`
		LogLevel       string `yaml:"log_level"`
	} `yaml:"srs"`
	Statistics struct {
		ActiveTTLSeconds         int `yaml:"active_ttl_seconds"`
		ReconcileIntervalSeconds int `yaml:"reconcile_interval_seconds"`
	} `yaml:"statistics"`

	// Runtime-only values are deliberately not configurable in config.yaml.
	Server struct {
		Listen string `yaml:"-"`
	} `yaml:"-"`
	SRSAPI         string `yaml:"-"`
	SRSHTTP        string `yaml:"-"`
	HookSecret     string `yaml:"-"`
	PublicAddress  string `yaml:"-"`
	RTMPPublicPort int    `yaml:"-"`
}

func parseConfig(data []byte) (Config, error) {
	var parsed Config
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&parsed); err != nil {
		return Config{}, fmt.Errorf("decode YAML: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Config{}, errors.New("config.yaml must contain exactly one YAML document")
		}
		return Config{}, fmt.Errorf("decode trailing YAML: %w", err)
	}
	if parsed.Version != 1 {
		return Config{}, fmt.Errorf("unsupported config version %d; expected 1", parsed.Version)
	}
	parsed.Admin.Username = strings.TrimSpace(parsed.Admin.Username)
	if parsed.Admin.Username == "" {
		return Config{}, errors.New("admin.username is required")
	}
	if parsed.Admin.Password == "" && strings.TrimSpace(parsed.Admin.PasswordHash) == "" {
		return Config{}, errors.New("admin.password or admin.password_hash is required")
	}
	if parsed.Session.MaxAge <= 0 {
		return Config{}, errors.New("session.max_age_seconds must be greater than zero")
	}
	if parsed.SRS.MaxConnections <= 0 {
		return Config{}, errors.New("srs.max_connections must be greater than zero")
	}
	if parsed.Statistics.ActiveTTLSeconds <= 0 {
		parsed.Statistics.ActiveTTLSeconds = 30
	}
	if parsed.Statistics.ReconcileIntervalSeconds <= 0 {
		parsed.Statistics.ReconcileIntervalSeconds = 15
	}
	parsed.SRS.LogLevel = strings.ToLower(strings.TrimSpace(parsed.SRS.LogLevel))
	if parsed.SRS.LogLevel == "" {
		parsed.SRS.LogLevel = "warn"
	}
	switch parsed.SRS.LogLevel {
	case "trace", "debug", "info", "warn", "error":
	default:
		return Config{}, errors.New("srs.log_level must be one of trace, debug, info, warn or error")
	}
	parsed.SRS.Candidate = strings.TrimSpace(parsed.SRS.Candidate)
	if strings.ContainsAny(parsed.SRS.Candidate, ";\r\n\t ") {
		return Config{}, errors.New("srs.candidate must be a single IP address without whitespace")
	}
	parsed.SRS.HookSecret = strings.TrimSpace(parsed.SRS.HookSecret)
	if matched, _ := regexp.MatchString(`^[0-9a-fA-F]+$`, parsed.SRS.HookSecret); !matched {
		return Config{}, errors.New("srs.hook_secret must contain only hexadecimal characters")
	}
	parsed.Server.Listen = ":8085"
	parsed.SRSAPI = "http://srs:1985"
	parsed.SRSHTTP = "http://srs:8080"
	parsed.HookSecret = parsed.SRS.HookSecret
	parsed.PublicAddress = strings.TrimSpace(parsed.Public.Address)
	parsed.RTMPPublicPort = 1935
	if value := strings.TrimSpace(os.Getenv("RTMP_PORT")); value != "" {
		port, err := strconv.Atoi(value)
		if err != nil || port < 1 || port > 65535 {
			return Config{}, errors.New("RTMP_PORT must be an integer between 1 and 65535")
		}
		parsed.RTMPPublicPort = port
	}
	return parsed, nil
}

func loadConfig(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	parsed, err := parseConfig(data)
	if err != nil {
		return err
	}
	replaceConfig(parsed)
	return nil
}

type initOptions struct {
	configPath   string
	templatePath string
	dataPath     string
	srsOutput    string
	srsTemplate  string
}

func parseInitOptions(args []string) (initOptions, error) {
	options := initOptions{
		configPath: defaultConfigPath, templatePath: defaultTemplatePath,
		dataPath: defaultDataPath, srsOutput: defaultSRSOutput, srsTemplate: defaultSRSTemplate,
	}
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&options.configPath, "config", options.configPath, "config.yaml path")
	flags.StringVar(&options.templatePath, "template", options.templatePath, "config template path")
	flags.StringVar(&options.dataPath, "data", options.dataPath, "data directory")
	flags.StringVar(&options.srsOutput, "srs-output", options.srsOutput, "rendered SRS config path")
	flags.StringVar(&options.srsTemplate, "srs-template", options.srsTemplate, "SRS template path")
	if err := flags.Parse(args); err != nil {
		return initOptions{}, err
	}
	return options, nil
}

func runInit(args []string) error {
	options, err := parseInitOptions(args)
	if err != nil {
		return err
	}
	generatedPassword, config, err := initializeConfig(options.configPath, options.templatePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(options.dataPath, "hls"), 0755); err != nil {
		return fmt.Errorf("create data directories: %w", err)
	}
	if err := renderSRSConfig(options.srsTemplate, options.srsOutput, config); err != nil {
		return err
	}
	if generatedPassword {
		fmt.Printf("SRS Token Gate initial admin username: %s\n", config.Admin.Username)
		fmt.Printf("SRS Token Gate initial admin password: %s\n", config.Admin.Password)
	}
	fmt.Println("SRS Token Gate initialization complete.")
	return nil
}

func showAdminPassword(args []string) error {
	flags := flag.NewFlagSet("show-admin-password", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config", defaultConfigPath, "config.yaml path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	data, err := os.ReadFile(*path)
	if err != nil {
		return err
	}
	config, err := parseConfig(data)
	if err != nil {
		return err
	}
	fmt.Printf("SRS Token Gate admin username: %s\n", config.Admin.Username)
	if config.Admin.PasswordHash != "" {
		fmt.Println("Administrator authentication uses admin.password_hash; no plaintext password is stored.")
		return nil
	}
	fmt.Printf("SRS Token Gate admin password: %s\n", config.Admin.Password)
	return nil
}

func initializeConfig(configPath, templatePath string) (bool, Config, error) {
	data, err := os.ReadFile(configPath)
	created := false
	if os.IsNotExist(err) {
		data, err = os.ReadFile(templatePath)
		created = true
	}
	if err != nil {
		return false, Config{}, fmt.Errorf("read configuration: %w", err)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return false, Config{}, fmt.Errorf("decode YAML node: %w", err)
	}
	var raw Config
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&raw); err != nil {
		return false, Config{}, fmt.Errorf("decode YAML: %w", err)
	}
	changed := created
	if raw.Version == 0 {
		raw.Version = 1
		setYAMLScalar(&document, []string{"version"}, "1", "!!int")
		changed = true
	}
	if strings.TrimSpace(raw.Admin.Username) == "" {
		raw.Admin.Username = "admin"
		setYAMLScalar(&document, []string{"admin", "username"}, raw.Admin.Username, "!!str")
		changed = true
	}
	generatedPassword := false
	if raw.Admin.Password == "" && strings.TrimSpace(raw.Admin.PasswordHash) == "" {
		raw.Admin.Password, err = randomHex(18)
		if err != nil {
			return false, Config{}, err
		}
		setYAMLScalar(&document, []string{"admin", "password"}, raw.Admin.Password, "!!str")
		generatedPassword = true
		changed = true
	}
	if strings.TrimSpace(raw.SRS.HookSecret) == "" {
		raw.SRS.HookSecret, err = randomHex(32)
		if err != nil {
			return false, Config{}, err
		}
		setYAMLScalar(&document, []string{"srs", "hook_secret"}, raw.SRS.HookSecret, "!!str")
		changed = true
	}
	if changed {
		encoded, err := encodeYAMLDocument(&document)
		if err != nil {
			return false, Config{}, err
		}
		if err := writeFileAtomic(configPath, encoded, 0600); err != nil {
			return false, Config{}, err
		}
	} else if err := os.Chmod(configPath, 0600); err != nil {
		return false, Config{}, fmt.Errorf("set config permissions: %w", err)
	}
	finalData, err := os.ReadFile(configPath)
	if err != nil {
		return false, Config{}, err
	}
	parsed, err := parseConfig(finalData)
	if err != nil {
		return false, Config{}, err
	}
	return generatedPassword, parsed, nil
}

func randomHex(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate random secret: %w", err)
	}
	return hex.EncodeToString(buffer), nil
}

func encodeYAMLDocument(document *yaml.Node) ([]byte, error) {
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		return nil, fmt.Errorf("encode YAML: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("close YAML encoder: %w", err)
	}
	return output.Bytes(), nil
}

func setYAMLScalar(document *yaml.Node, path []string, value, tag string) {
	if len(document.Content) == 0 {
		return
	}
	node := document.Content[0]
	for _, key := range path {
		if node.Kind != yaml.MappingNode {
			return
		}
		var next *yaml.Node
		for index := 0; index+1 < len(node.Content); index += 2 {
			if node.Content[index].Value == key {
				next = node.Content[index+1]
				break
			}
		}
		if next == nil {
			keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
			next = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			node.Content = append(node.Content, keyNode, next)
		}
		node = next
	}
	node.Kind = yaml.ScalarNode
	node.Tag = tag
	node.Value = value
	node.Style = 0
}

func renderSRSConfig(templatePath, outputPath string, config Config) error {
	template, err := os.ReadFile(templatePath)
	if err != nil {
		return fmt.Errorf("read SRS template: %w", err)
	}
	candidate := config.SRS.Candidate
	if candidate == "" {
		candidate = "*"
	}
	rendered := strings.NewReplacer(
		"__SRS_HOOK_SECRET__", config.SRS.HookSecret,
		"__SRS_CANDIDATE__", candidate,
		"__SRS_MAX_CONNECTIONS__", strconv.Itoa(config.SRS.MaxConnections),
		"__SRS_LOG_LEVEL__", config.SRS.LogLevel,
	).Replace(string(template))
	if strings.Contains(rendered, "__SRS_") {
		return errors.New("SRS template contains an unresolved placeholder")
	}
	return writeFileAtomic(outputPath, []byte(rendered), 0600)
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create parent directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return os.Chmod(path, mode)
}
