package config

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

func (c TLSConfig) ServerTLSConfig() (*tls.Config, error) {
	if !c.EnabledForServer() {
		return nil, nil
	}

	certificate, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load tls key pair: %w", err)
	}

	cfg := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{certificate},
	}

	if c.RequireClientCert {
		pool, err := loadCertPool(c.CAFile)
		if err != nil {
			return nil, err
		}
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
		cfg.ClientCAs = pool
	}

	return cfg, nil
}

func (c TLSConfig) ClientTLSConfig() (*tls.Config, error) {
	if !c.EnabledForClient() {
		return nil, nil
	}

	cfg := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: c.InsecureSkipVerify,
	}
	if c.ServerName != "" {
		cfg.ServerName = c.ServerName
	}
	if c.CertFile != "" || c.KeyFile != "" {
		certificate, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load tls key pair: %w", err)
		}
		cfg.Certificates = []tls.Certificate{certificate}
	}
	if c.CAFile != "" {
		pool, err := loadCertPool(c.CAFile)
		if err != nil {
			return nil, err
		}
		cfg.RootCAs = pool
	}

	return cfg, nil
}

func loadCertPool(path string) (*x509.CertPool, error) {
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read tls ca file %s: %w", path, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("parse tls ca file %s: no certificates found", path)
	}
	return pool, nil
}
