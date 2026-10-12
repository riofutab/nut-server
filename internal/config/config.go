package config

import (
	"bytes"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultShutdownCommandTimeout bounds how long a shutdown command may run before
// it is killed and reported as failed. It is generous enough for any real
// shutdown (`/sbin/shutdown -h now` returns near-instantly) while turning a hung
// custom script into a bounded, retriable failure instead of a permanent stall.
const DefaultShutdownCommandTimeout = 2 * time.Minute

// maxChargePercent is the upper bound of a charge_below threshold.
const maxChargePercent = 100

// Units a runtime OID may report in; the master converts them to minutes.
const (
	RuntimeUnitMinutes   = "minutes"
	RuntimeUnitSeconds   = "seconds"
	RuntimeUnitTimeTicks = "timeticks" // 1/100 s
)

// Enrollment modes for slaves without an issued per-node token.
const (
	EnrollmentOpen   = "open"
	EnrollmentClosed = "closed"
)

type MasterConfig struct {
	ListenAddr      string         `yaml:"listen_addr"`
	AdminListenAddr string         `yaml:"admin_listen_addr"`
	AdminToken      string         `yaml:"admin_token"`
	AdminTLS        AdminTLSConfig `yaml:"admin_tls"`
	PublicAddr      string         `yaml:"public_addr"`
	InstallRepo     string         `yaml:"install_repo"`
	// InstallCAFile is the CA the install script writes to slaves as their
	// trust anchor. Empty embeds tls.cert_file, which slaves then pin until the
	// master certificate is replaced.
	InstallCAFile string `yaml:"install_ca_file"`
	StateFile     string `yaml:"state_file"`
	// EnrollmentTokens admit a node_id that has no issued per-node token yet;
	// the master then issues it one and the enrollment token can no longer
	// register as that node.
	EnrollmentTokens []string             `yaml:"enrollment_tokens"`
	Enrollment       string               `yaml:"enrollment"`
	PollInterval     Duration             `yaml:"poll_interval"`
	CommandTimeout   Duration             `yaml:"command_timeout"`
	OfflineAfter     Duration             `yaml:"offline_after"`
	DryRun           bool                 `yaml:"dry_run"`
	LogUPSStatus     bool                 `yaml:"log_ups_status"`
	TLS              TLSConfig            `yaml:"tls"`
	LocalShutdown    LocalShutdownConfig  `yaml:"local_shutdown"`
	ShutdownPolicies []ShutdownPolicySpec `yaml:"shutdown_policies"`
	SNMP             SNMPConfig           `yaml:"snmp"`
}

type AdminTLSConfig struct {
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

type ShutdownPolicySpec struct {
	Name string             `yaml:"name"`
	When ShutdownPolicyWhen `yaml:"when"`
	// Duration is how long the conditions must hold continuously before the
	// policy fires; zero fires on the first matching poll.
	Duration Duration             `yaml:"duration"`
	Target   ShutdownPolicyTarget `yaml:"target"`
	Reason   string               `yaml:"reason"`
}

// ShutdownPolicyWhen is evaluated only while the UPS is on battery, so an
// empty When means "on battery". UPSUnreachableFor is exclusive: it fires when
// the UPS has not answered for that long since a battery reading.
type ShutdownPolicyWhen struct {
	ChargeBelow       int      `yaml:"charge_below"`
	RuntimeBelow      int      `yaml:"runtime_below"`
	UPSUnreachableFor Duration `yaml:"ups_unreachable_for"`
}

type ShutdownPolicyTarget struct {
	All     bool     `yaml:"all"`
	Tags    []string `yaml:"tags"`
	NodeIDs []string `yaml:"node_ids"`
}

type LocalShutdownConfig struct {
	Enabled                 bool     `yaml:"enabled"`
	Command                 []string `yaml:"command"`
	CommandTimeout          Duration `yaml:"command_timeout"`
	MaxWait                 Duration `yaml:"max_wait"`
	EmergencyRuntimeMinutes int      `yaml:"emergency_runtime_minutes"`
	// UnreachableAfter powers the master off when the UPS stops answering
	// during an on-battery episode; defaults to MaxWait.
	UnreachableAfter Duration `yaml:"unreachable_after"`
}

type SNMPConfig struct {
	Target         string `yaml:"target"`
	Port           uint16 `yaml:"port"`
	Version        string `yaml:"version"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
	// Community is used by SNMP v1 / v2c.
	Community string `yaml:"community"`
	// USM settings, used by SNMP v3.
	Username       string `yaml:"username"`
	SecurityLevel  string `yaml:"security_level"`
	AuthProtocol   string `yaml:"auth_protocol"`
	AuthPassphrase string `yaml:"auth_passphrase"`
	PrivProtocol   string `yaml:"priv_protocol"`
	PrivPassphrase string `yaml:"priv_passphrase"`
	// Profile selects vendor OID presets; OnBattery / Charge / Runtime
	// override single entries. After loading all three are always set.
	Profile   string        `yaml:"profile"`
	OnBattery *SNMPStateVar `yaml:"on_battery"`
	Charge    *SNMPValueVar `yaml:"charge"`
	Runtime   *SNMPValueVar `yaml:"runtime"`
}

// SNMPStateVar is an OID whose value is compared against Values.
type SNMPStateVar struct {
	OID    string `yaml:"oid"`
	Values []int  `yaml:"values"`
}

// SNMPValueVar is a numeric OID; Unit applies to runtime only.
type SNMPValueVar struct {
	OID  string `yaml:"oid"`
	Unit string `yaml:"unit"`
}

type SlaveConfig struct {
	NodeID                 string    `yaml:"node_id"`
	MasterAddr             string    `yaml:"master_addr"`
	Token                  string    `yaml:"token"`
	Tags                   []string  `yaml:"tags"`
	StateFile              string    `yaml:"state_file"`
	MetricsListenAddr      string    `yaml:"metrics_listen_addr"`
	ReconnectInterval      Duration  `yaml:"reconnect_interval"`
	DryRun                 bool      `yaml:"dry_run"`
	TLS                    TLSConfig `yaml:"tls"`
	ShutdownCommand        []string  `yaml:"shutdown_command"`
	ShutdownCommandTimeout Duration  `yaml:"shutdown_command_timeout"`
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
	// BindNodeIDToCert, when true, requires a registering slave's node_id to
	// match its client certificate CommonName or a DNS SAN (mTLS only). Opt-in
	// so existing deployments whose cert CN is not the node_id are unaffected.
	BindNodeIDToCert bool `yaml:"bind_node_id_to_cert"`
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
	if c.BindNodeIDToCert && !c.RequireClientCert {
		return fmt.Errorf("tls.bind_node_id_to_cert requires tls.require_client_cert: true; without client certificates there is nothing to bind the node_id to")
	}
	if c.Disabled {
		if c.Enabled || c.CertFile != "" || c.KeyFile != "" || c.CAFile != "" || c.RequireClientCert {
			return fmt.Errorf("tls.disabled cannot be combined with other tls.* settings; remove disabled or clear enabled/cert_file/key_file/ca_file/require_client_cert")
		}
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
	if c.CAFile != "" && !c.RequireClientCert {
		return fmt.Errorf("tls.ca_file requires tls.require_client_cert: true; on the master the CA is only used to verify client certificates")
	}
	return nil
}

func (c TLSConfig) ValidateClient() error {
	if c.Disabled {
		if c.Enabled || c.CertFile != "" || c.KeyFile != "" || c.CAFile != "" || c.ServerName != "" || c.InsecureSkipVerify {
			return fmt.Errorf("tls.disabled cannot be combined with other tls.* settings; remove disabled or clear enabled/cert_file/key_file/ca_file/server_name/insecure_skip_verify")
		}
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
	root, err := loadYAML(path, &cfg, removedMasterKeys)
	if err != nil {
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
	for i, token := range cfg.EnrollmentTokens {
		if err := validateSecret(fmt.Sprintf("enrollment_tokens[%d]", i), token); err != nil {
			return MasterConfig{}, err
		}
	}
	switch cfg.Enrollment {
	case "":
		cfg.Enrollment = EnrollmentOpen
	case EnrollmentOpen, EnrollmentClosed:
	default:
		return MasterConfig{}, fmt.Errorf("enrollment %q is invalid; use %q or %q", cfg.Enrollment, EnrollmentOpen, EnrollmentClosed)
	}
	if cfg.Enrollment == EnrollmentOpen && len(cfg.EnrollmentTokens) == 0 {
		return MasterConfig{}, fmt.Errorf("enrollment_tokens must not be empty while enrollment is open")
	}
	if cfg.AdminToken == "" {
		return MasterConfig{}, fmt.Errorf("admin_token must not be empty")
	}
	if err := validateSecret("admin_token", cfg.AdminToken); err != nil {
		return MasterConfig{}, err
	}
	if cfg.PollInterval.Duration == 0 {
		cfg.PollInterval.Duration = 10 * time.Second
	}
	if cfg.CommandTimeout.Duration == 0 {
		cfg.CommandTimeout.Duration = 30 * time.Second
	}
	if cfg.OfflineAfter.Duration == 0 {
		cfg.OfflineAfter.Duration = 45 * time.Second
	}
	if cfg.LocalShutdown.Enabled && cfg.LocalShutdown.Command != nil && len(cfg.LocalShutdown.Command) == 0 {
		return MasterConfig{}, fmt.Errorf("local_shutdown.command must not be empty when local_shutdown.enabled is true")
	}
	if len(cfg.LocalShutdown.Command) == 0 {
		cfg.LocalShutdown.Command = []string{"/sbin/shutdown", "-h", "now"}
	}
	// An explicit zero must fail rather than silently become the default.
	if findKey(root, "local_shutdown", "max_wait") != nil && cfg.LocalShutdown.MaxWait.Duration <= 0 {
		return MasterConfig{}, fmt.Errorf("local_shutdown.max_wait must be greater than zero")
	}
	if cfg.LocalShutdown.MaxWait.Duration == 0 {
		cfg.LocalShutdown.MaxWait.Duration = 15 * time.Minute
	}
	if findKey(root, "local_shutdown", "emergency_runtime_minutes") != nil && cfg.LocalShutdown.EmergencyRuntimeMinutes <= 0 {
		return MasterConfig{}, fmt.Errorf("local_shutdown.emergency_runtime_minutes must be greater than zero")
	}
	if cfg.LocalShutdown.EmergencyRuntimeMinutes == 0 {
		cfg.LocalShutdown.EmergencyRuntimeMinutes = 15
	}
	if cfg.LocalShutdown.CommandTimeout.Duration < 0 {
		return MasterConfig{}, fmt.Errorf("local_shutdown.command_timeout must not be negative")
	}
	if cfg.LocalShutdown.CommandTimeout.Duration == 0 {
		cfg.LocalShutdown.CommandTimeout.Duration = DefaultShutdownCommandTimeout
	}
	if err := cfg.TLS.ValidateServer(); err != nil {
		return MasterConfig{}, err
	}
	if (cfg.AdminTLS.CertFile == "") != (cfg.AdminTLS.KeyFile == "") {
		return MasterConfig{}, fmt.Errorf("admin_tls.cert_file and admin_tls.key_file must be set together")
	}
	if err := validateInstallCAFile(cfg); err != nil {
		return MasterConfig{}, err
	}
	if cfg.SNMP.Target == "" {
		return MasterConfig{}, fmt.Errorf("snmp.target must not be empty")
	}
	if cfg.SNMP.Port == 0 {
		cfg.SNMP.Port = 161
	}
	if err := normalizeSNMPVersion(&cfg.SNMP); err != nil {
		return MasterConfig{}, err
	}
	if cfg.SNMP.TimeoutSeconds == 0 {
		cfg.SNMP.TimeoutSeconds = 2
	}
	if err := ResolveSNMPVars(&cfg.SNMP); err != nil {
		return MasterConfig{}, err
	}
	if cfg.LocalShutdown.UnreachableAfter.Duration < 0 {
		return MasterConfig{}, fmt.Errorf("local_shutdown.unreachable_after must not be negative")
	}
	if cfg.LocalShutdown.UnreachableAfter.Duration == 0 {
		cfg.LocalShutdown.UnreachableAfter = cfg.LocalShutdown.MaxWait
	}
	if err := normalizeShutdownPolicies(&cfg); err != nil {
		return MasterConfig{}, err
	}
	return cfg, nil
}

func validateInstallCAFile(cfg MasterConfig) error {
	if cfg.InstallCAFile == "" {
		return nil
	}
	if !cfg.TLS.EnabledForServer() {
		return fmt.Errorf("install_ca_file requires TLS on the slave listener (tls.enabled)")
	}
	content, err := os.ReadFile(cfg.InstallCAFile)
	if err != nil {
		return fmt.Errorf("read install_ca_file: %w", err)
	}
	if !x509.NewCertPool().AppendCertsFromPEM(content) {
		return fmt.Errorf("install_ca_file %s contains no PEM certificate", cfg.InstallCAFile)
	}
	return nil
}

func normalizeShutdownPolicies(cfg *MasterConfig) error {
	if len(cfg.ShutdownPolicies) == 0 {
		return fmt.Errorf("shutdown_policies must define at least one policy")
	}
	seen := make(map[string]struct{}, len(cfg.ShutdownPolicies))
	for i, p := range cfg.ShutdownPolicies {
		if p.Name == "" {
			return fmt.Errorf("shutdown_policies[%d].name must not be empty", i)
		}
		if _, dup := seen[p.Name]; dup {
			return fmt.Errorf("shutdown_policies[%d].name %q is duplicated", i, p.Name)
		}
		seen[p.Name] = struct{}{}
		if err := validatePolicyConditions(p); err != nil {
			return fmt.Errorf("shutdown_policies[%d] (%s).%w", i, p.Name, err)
		}
		if !p.Target.All && len(p.Target.Tags) == 0 && len(p.Target.NodeIDs) == 0 {
			cfg.ShutdownPolicies[i].Target.All = true
		}
		if cfg.ShutdownPolicies[i].Reason == "" {
			cfg.ShutdownPolicies[i].Reason = fmt.Sprintf("policy %s triggered", p.Name)
		}
	}
	return nil
}

// validatePolicyConditions returns errors starting with the offending key so
// the caller can prefix the policy path.
func validatePolicyConditions(p ShutdownPolicySpec) error {
	when := p.When
	if when.ChargeBelow < 0 || when.ChargeBelow > maxChargePercent {
		return fmt.Errorf("when.charge_below must be between 0 and %d", maxChargePercent)
	}
	if when.RuntimeBelow < 0 {
		return fmt.Errorf("when.runtime_below must not be negative")
	}
	if p.Duration.Duration < 0 {
		return fmt.Errorf("duration must not be negative")
	}
	if when.UPSUnreachableFor.Duration < 0 {
		return fmt.Errorf("when.ups_unreachable_for must not be negative")
	}
	if when.UPSUnreachableFor.Duration == 0 {
		return nil
	}
	// The UPS gives no readings while unreachable, so other conditions could
	// never be evaluated, and the timeout already acts as the duration.
	if when.ChargeBelow != 0 || when.RuntimeBelow != 0 {
		return fmt.Errorf("when.ups_unreachable_for cannot be combined with charge_below or runtime_below; put it in its own policy")
	}
	if p.Duration.Duration != 0 {
		return fmt.Errorf("when.ups_unreachable_for cannot be combined with duration; it is already a duration")
	}
	return nil
}

func LoadSlaveConfig(path string) (SlaveConfig, error) {
	var cfg SlaveConfig
	if _, err := loadYAML(path, &cfg, nil); err != nil {
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
	if err := validateSecret("token", cfg.Token); err != nil {
		return SlaveConfig{}, err
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
	if cfg.ShutdownCommandTimeout.Duration < 0 {
		return SlaveConfig{}, fmt.Errorf("shutdown_command_timeout must not be negative")
	}
	if cfg.ShutdownCommandTimeout.Duration == 0 {
		cfg.ShutdownCommandTimeout.Duration = DefaultShutdownCommandTimeout
	}
	return cfg, nil
}

// placeholderSecrets are the example values shipped in configs/*.example.yaml.
// Refusing them at load time stops a copied-but-unedited config from running
// with publicly-known, fleet-wide credentials.
var placeholderSecrets = map[string]struct{}{
	"replace-with-strong-token":       {},
	"replace-with-strong-admin-token": {},
}

func validateSecret(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must not be blank", field)
	}
	if _, ok := placeholderSecrets[value]; ok {
		return fmt.Errorf("%s is still the example placeholder %q; set a strong unique secret", field, value)
	}
	return nil
}

// removedKey is a config key dropped by a breaking release. Path elements
// are mapping keys; "*" matches every item of a sequence.
type removedKey struct {
	path []string
	hint string
}

var removedMasterKeys = []removedKey{
	{path: []string{"auth_tokens"}, hint: "use enrollment_tokens"},
	{path: []string{"shutdown_policy"}, hint: "use shutdown_policies"},
	{path: []string{"shutdown_policies", "*", "when", "on_battery"}, hint: "policies are only evaluated while on battery; delete it"},
	{path: []string{"snmp", "output_source_oid"}, hint: "use snmp.profile or snmp.on_battery"},
	{path: []string{"snmp", "charge_oid"}, hint: "use snmp.profile or snmp.charge"},
	{path: []string{"snmp", "runtime_minutes_oid"}, hint: "use snmp.profile or snmp.runtime"},
}

// loadYAML decodes path into dst, rejecting unknown keys. removed keys are
// checked first so they get an upgrade hint instead of the generic
// "field not found". The returned root node lets callers tell an explicit
// zero from an absent key.
func loadYAML(path string, dst any, removed []removedKey) (*yaml.Node, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(content, &root); err != nil {
		return nil, fmt.Errorf("decode config %s: %w", path, err)
	}
	for _, key := range removed {
		if node, label := lookupKey(&root, key.path, ""); node != nil {
			return nil, fmt.Errorf("decode config %s: line %d: %s was removed in v0.6; %s (see docs/upgrade.md)", path, node.Line, label, key.hint)
		}
	}
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decoder.KnownFields(true)
	// io.EOF means an empty file; validation then reports the missing fields.
	if err := decoder.Decode(dst); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("decode config %s: %w", path, err)
	}
	return &root, nil
}

// lookupKey returns the key node at path and its dotted label with
// sequence indexes filled in, e.g. "shutdown_policies[1].when.on_battery".
func lookupKey(node *yaml.Node, path []string, label string) (*yaml.Node, string) {
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		return lookupKey(node.Content[0], path, label)
	}
	if path[0] == "*" {
		if node.Kind != yaml.SequenceNode {
			return nil, ""
		}
		for i, item := range node.Content {
			if found, foundLabel := lookupKey(item, path[1:], fmt.Sprintf("%s[%d]", label, i)); found != nil {
				return found, foundLabel
			}
		}
		return nil, ""
	}
	if label != "" {
		label += "."
	}
	label += path[0]
	key, value := mappingEntry(node, path[0])
	if key == nil || len(path) == 1 {
		return key, label
	}
	return lookupKey(value, path[1:], label)
}

// findKey returns the key node at a path of mapping keys, or nil.
func findKey(root *yaml.Node, path ...string) *yaml.Node {
	node, _ := lookupKey(root, path, "")
	return node
}

func mappingEntry(node *yaml.Node, key string) (*yaml.Node, *yaml.Node) {
	if node.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i], node.Content[i+1]
		}
	}
	return nil, nil
}
