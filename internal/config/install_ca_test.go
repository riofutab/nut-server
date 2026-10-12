package config

import (
	"strings"
	"testing"
)

func tlsMasterFields(certFile, keyFile string) string {
	return "tls:\n  enabled: true\n  cert_file: \"" + certFile + "\"\n  key_file: \"" + keyFile + "\"\n"
}

func TestLoadMasterConfigAcceptsInstallCAFileWithTLS(t *testing.T) {
	caFile, certFile, keyFile := writeTLSFiles(t, t.TempDir(), "master")
	path := writeConfigFile(t, minimalMasterFields+defaultPolicy+tlsMasterFields(certFile, keyFile)+"install_ca_file: \""+caFile+"\"\n")

	cfg, err := LoadMasterConfig(path)

	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.InstallCAFile != caFile {
		t.Fatalf("install_ca_file=%q want %q", cfg.InstallCAFile, caFile)
	}
}

func TestLoadMasterConfigRejectsInstallCAFileWithoutTLS(t *testing.T) {
	caFile, _, _ := writeTLSFiles(t, t.TempDir(), "master")
	path := writeConfigFile(t, minimalMasterFields+defaultPolicy+"tls:\n  disabled: true\ninstall_ca_file: \""+caFile+"\"\n")

	_, err := LoadMasterConfig(path)

	if err == nil || !strings.Contains(err.Error(), "install_ca_file requires TLS") {
		t.Fatalf("expected install_ca_file without TLS to fail, got %v", err)
	}
}

func TestLoadMasterConfigRejectsInstallCAFileWithoutCertificate(t *testing.T) {
	dir := t.TempDir()
	_, certFile, keyFile := writeTLSFiles(t, dir, "master")
	notPEM := writeTestFile(t, dir, "ca.txt", "not a certificate")
	path := writeConfigFile(t, minimalMasterFields+defaultPolicy+tlsMasterFields(certFile, keyFile)+"install_ca_file: \""+notPEM+"\"\n")

	_, err := LoadMasterConfig(path)

	if err == nil || !strings.Contains(err.Error(), "install_ca_file") {
		t.Fatalf("expected a non-PEM install_ca_file to fail, got %v", err)
	}
}
