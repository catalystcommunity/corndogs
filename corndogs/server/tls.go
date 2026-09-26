package server

import (
	"crypto/tls"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/CatalystCommunity/corndogs/corndogs/server/config"
	zlog "github.com/rs/zerolog/log"
)

// TLS for the CSIL-RPC listener. When CORNDOGS_TLS_CERT_FILE and
// CORNDOGS_TLS_KEY_FILE are set, the server accepts TLS only (TLS 1.2 or
// later) on CORNDOGS_LISTEN. The server does not ask for a client certificate.
var (
	tlsCertFile       = config.GetEnvOrDefault("CORNDOGS_TLS_CERT_FILE", "")
	tlsKeyFile        = config.GetEnvOrDefault("CORNDOGS_TLS_KEY_FILE", "")
	tlsReloadInterval = config.GetEnvOrDefault("CORNDOGS_TLS_RELOAD_INTERVAL", "10s")
)

// certReloader gives the current certificate to each TLS handshake. It examines
// the certificate and key files at most once per interval, and loads them again
// when either file changed. Kubernetes updates a mounted Secret by an atomic
// symlink swap; os.Stat follows the symlink, so the swap shows as a change. If
// a load fails (for example, the new key does not match the new certificate),
// the reloader keeps the last good certificate and logs the error.
type certReloader struct {
	certFile, keyFile string
	interval          time.Duration

	mu        sync.Mutex
	cert      *tls.Certificate
	certStat  os.FileInfo
	keyStat   os.FileInfo
	lastCheck time.Time
	now       func() time.Time // replaced in tests
}

func newCertReloader(certFile, keyFile string, interval time.Duration) (*certReloader, error) {
	r := &certReloader{certFile: certFile, keyFile: keyFile, interval: interval, now: time.Now}
	if err := r.load(); err != nil {
		return nil, err
	}
	r.lastCheck = r.now()
	return r, nil
}

// load reads both files and replaces the certificate. The caller holds mu, or
// no other goroutine can see r yet.
func (r *certReloader) load() error {
	certStat, err := os.Stat(r.certFile)
	if err != nil {
		return err
	}
	keyStat, err := os.Stat(r.keyFile)
	if err != nil {
		return err
	}
	cert, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return err
	}
	r.cert, r.certStat, r.keyStat = &cert, certStat, keyStat
	return nil
}

func changed(old os.FileInfo, path string) bool {
	cur, err := os.Stat(path)
	if err != nil {
		return false // a file in the middle of a swap; examine it at the next check
	}
	return !os.SameFile(old, cur) || !cur.ModTime().Equal(old.ModTime()) || cur.Size() != old.Size()
}

// GetCertificate implements tls.Config.GetCertificate.
func (r *certReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if now := r.now(); now.Sub(r.lastCheck) >= r.interval {
		r.lastCheck = now
		if changed(r.certStat, r.certFile) || changed(r.keyStat, r.keyFile) {
			if err := r.load(); err != nil {
				zlog.Error().Err(err).Str("cert", r.certFile).Str("key", r.keyFile).
					Msg("tls: reload failed; keeping the previous certificate")
			} else {
				zlog.Info().Str("cert", r.certFile).Msg("tls: certificate reloaded")
			}
		}
	}
	return r.cert, nil
}

// rpcTLSConfig returns the TLS configuration for the RPC listener, or nil when
// TLS is not configured.
func rpcTLSConfig() (*tls.Config, error) {
	if tlsCertFile == "" && tlsKeyFile == "" {
		return nil, nil
	}
	if tlsCertFile == "" || tlsKeyFile == "" {
		return nil, fmt.Errorf("tls: set both CORNDOGS_TLS_CERT_FILE and CORNDOGS_TLS_KEY_FILE")
	}
	interval, err := time.ParseDuration(tlsReloadInterval)
	if err != nil || interval < 0 {
		return nil, fmt.Errorf("tls: CORNDOGS_TLS_RELOAD_INTERVAL %q is not a valid duration", tlsReloadInterval)
	}
	r, err := newCertReloader(tlsCertFile, tlsKeyFile, interval)
	if err != nil {
		return nil, fmt.Errorf("tls: load certificate: %w", err)
	}
	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: r.GetCertificate,
	}, nil
}
