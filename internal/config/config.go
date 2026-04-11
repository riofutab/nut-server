package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type MasterConfig struct {
	ListenAddr      string         `yaml:"listen_addr"`
	AdminListenAddr string         `yaml:"admin_listen_addr"`
	StateFile       string         `yaml:"state_file"`
	AuthTokens      []string       `yaml:"auth_tokens"`
	PollInterval    Duration       `yaml:"poll_interval"`
	CommandTimeout  Duration       `yaml:"command_timeout"`
	DryRun          bool           `yaml:"dry_run"`
	LogUPSStatus    bool           `yaml:"log_ups_status"`
	TLS             TLSConfig      `yaml:"tls"`
	ShutdownPolicy  ShutdownPolicy `yaml:"shutdown_policy"`
	SNMP            SNMPConfig     `yaml:"snmp"`
}

type ShutdownPolicy struct {
	RequireOnBattery  bool   `yaml:"require_on_battery"`
	MinBatteryCharge  int    `yaml:"min_battery_charge"`
	MinRuntimeMinutes int    `yaml:"min_runtime_minutes"`
	ShutdownReason    string `yaml:"shutdown_reason"`
}

type SNMPConfig struct {
	Target            string `yaml:"target"`
	Port              uint16 `yaml:"port"`
	Community         string `yaml:"community"`
	Version           string `yaml:"version"`
	TimeoutSeconds    int    `yaml:"timeout_seconds"`
	OutputSourceOID   string `yaml:"output_source_oid"`
	ChargeOID         string `yaml:"charge_oid"`
	RuntimeMinutesOID string `yaml:"runtime_minutes_oid"`
}

type SlaveConfig struct {
	NodeID            string    `yaml:"node_id"`
	MasterAddr        string    `yaml:"master_addr"`
	Token             string    `yaml:"token"`
	Tags              []string  `yaml:"tags"`
	StateFile         string    `yaml:"state_file"`
	ReconnectInterval Duration  `yaml:"reconnect_interval"`
	DryRun            bool      `yaml:"dry_run"`
	TLS               TLSConfig `yaml:"tls"`
	ShutdownCommand   []string  `yaml:"shutdown_command"`
}

type TLSConfig struct {
	Enabled            bool   `yaml:"enabled"`
	Disabled           bool   `yaml:"disabled"`
	CertFile           string `yaml:"cert_file"`
	KeyFile            string `yaml:"key_file"`
	CAFile             string `yaml:"ca_file"`
	ServerName         string `yaml:"server_name"`
	RequireClientCert  bool   `yaml:"require_client_cert"`
	InsecureSkipVerify bool   `yaml:"insecure_skip_verify"`
}

type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var raw string
	if err := value.Decode(&raw); err != nil {
		return err
	}
	duration, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("parse duration %q: %w", raw, err)
	}
	d.Duration = duration
	return nil
}

func (c TLSConfig) EnabledForServer() bool {
	if c.Disabled {
		return false
	}
	return c.Enabled || c.CertFile != "" || c.KeyFile != "" || c.CAFile != "" || c.RequireClientCert
}

func (c TLSConfig) EnabledForClient() bool {
	if c.Disabled {
		return false
	}
	return c.Enabled || c.CertFile != "" || c.KeyFile != "" || c.CAFile != "" || c.ServerName != "" || c.InsecureSkipVerify
}

func (c TLSConfig) ValidateServer() error {
	if c.Disabled {
		return nil
	}
	if !c.EnabledForServer() {
		return nil
	}
	if c.CertFile == "" {
		return fmt.Errorf("tls.cert_file must not be empty when TLS is enabled")
	}
	if c.KeyFile == "" {
		return fmt.Errorf("tls.key_file must not be empty when TLS is enabled")
	}
	if c.RequireClientCert && c.CAFile == "" {
		return fmt.Errorf("tls.ca_file must not be empty when tls.require_client_cert is true")
	}
	return nil
}

func (c TLSConfig) ValidateClient() error {
	if c.Disabled {
		return nil
	}
	if !c.EnabledForClient() {
		return nil
	}
	if c.CertFile != "" && c.KeyFile == "" {
		return fmt.Errorf("tls.key_file must not be empty when tls.cert_file is set")
	}
	if c.KeyFile != "" && c.CertFile == "" {
		return fmt.Errorf("tls.cert_file must not be empty when tls.key_file is set")
	}
	if c.InsecureSkipVerify && c.CAFile != "" {
		return fmt.Errorf("tls.ca_file cannot be combined with tls.insecure_skip_verify")
	}
	return nil
}

func LoadMasterConfig(path string) (MasterConfig, error) {
	var cfg MasterConfig
	if err := loadYAML(path, &cfg); err != nil {
		return MasterConfig{}, err
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":9000"
	}
	if cfg.AdminListenAddr == "" {
		cfg.AdminListenAddr = "127.0.0.1:9001"
	}
	if cfg.StateFile == "" {
		cfg.StateFile = "data/master-state.json"
	}
	if len(cfg.AuthTokens) == 0 {
		return MasterConfig{}, fmt.Errorf("auth_tokens must not be empty")
	}
	if cfg.PollInterval.Duration == 0 {
		cfg.PollInterval.Duration = 10 * time.Second
	}
	if cfg.CommandTimeout.Duration == 0 {
		cfg.CommandTimeout.Duration = 30 * time.Second
	}
	if err := cfg.TLS.ValidateServer(); err != nil {
		return MasterConfig{}, err
	}
	if cfg.SNMP.Target == "" {
		return MasterConfig{}, fmt.Errorf("snmp.target must not be empty")
	}
	if cfg.SNMP.Port == 0 {
		cfg.SNMP.Port = 161
	}
	if cfg.SNMP.TimeoutSeconds == 0 {
		cfg.SNMP.TimeoutSeconds = 2
	}
	if cfg.ShutdownPolicy.ShutdownReason == "" {
		cfg.ShutdownPolicy.ShutdownReason = "UPS battery threshold reached"
	}
	return cfg, nil
}

func LoadSlaveConfig(path string) (SlaveConfig, error) {
	var cfg SlaveConfig
	if err := loadYAML(path, &cfg); err != nil {
		return SlaveConfig{}, err
	}
	if cfg.NodeID == "" {
		return SlaveConfig{}, fmt.Errorf("node_id must not be empty")
	}
	if cfg.MasterAddr == "" {
		return SlaveConfig{}, fmt.Errorf("master_addr must not be empty")
	}
	if cfg.Token == "" {
		return SlaveConfig{}, fmt.Errorf("token must not be empty")
	}
	if cfg.ReconnectInterval.Duration == 0 {
		cfg.ReconnectInterval.Duration = 5 * time.Second
	}
	if cfg.StateFile == "" {
		cfg.StateFile = "data/slave-state.json"
	}
	if err := cfg.TLS.ValidateClient(); err != nil {
		return SlaveConfig{}, err
	}
	if len(cfg.ShutdownCommand) == 0 {
		cfg.ShutdownCommand = []string{"/sbin/shutdown", "-h", "now"}
	}
	return cfg, nil
}

func loadYAML(path string, dst interface{}) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config %s: %w", path, err)
	}
	if err := yaml.Unmarshal(content, dst); err != nil {
		return fmt.Errorf("decode config %s: %w", path, err)
	}
	return nil
}
