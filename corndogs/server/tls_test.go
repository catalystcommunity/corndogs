package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/CatalystCommunity/corndogs/corndogs/server/implementations"
	"github.com/stretchr/testify/require"
)

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "corndogs test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &testCA{cert: cert, key: key, pool: pool}
}

// issue writes a server certificate for 127.0.0.1 and its key into dir.
func (ca *testCA) issue(t *testing.T, dir string, serial int64) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: "corndogs"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tls.crt"),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tls.key"),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600))
}

// mountSecret lays out files the way the kubelet mounts a Secret: the visible
// names are symlinks through a "..data" symlink to a versioned directory, and an
// update swaps "..data" to a new directory.
func mountSecret(t *testing.T, mount, version string) {
	t.Helper()
	tmp := filepath.Join(mount, "..data_tmp")
	require.NoError(t, os.Symlink(version, tmp))
	require.NoError(t, os.Rename(tmp, filepath.Join(mount, "..data")))
	for _, name := range []string{"tls.crt", "tls.key"} {
		link := filepath.Join(mount, name)
		if _, err := os.Lstat(link); os.IsNotExist(err) {
			require.NoError(t, os.Symlink(filepath.Join("..data", name), link))
		}
	}
}

// startTLSServer serves CSIL-RPC over TLS with the given reloader and returns
// the address.
func startTLSServer(t *testing.T, r *certReloader) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	tlsLn := tls.NewListener(ln, &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: r.GetCertificate})
	go serveCSILRPCTCP(tlsLn, &implementations.V1Alpha1Server{})
	t.Cleanup(func() { _ = tlsLn.Close() })
	return ln.Addr().String()
}

func pingTLS(addr string, cfg *tls.Config) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tr := &api.StreamTransport{Addr: addr, TLSConfig: cfg}
	defer tr.Close()
	return tr.Ping(ctx)
}

// servedSerial returns the serial number of the certificate the server shows.
func servedSerial(t *testing.T, addr string, pool *x509.CertPool) int64 {
	t.Helper()
	conn, err := tls.Dial("tcp", addr, &tls.Config{RootCAs: pool})
	require.NoError(t, err)
	defer conn.Close()
	return conn.ConnectionState().PeerCertificates[0].SerialNumber.Int64()
}

func TestTLSServesAndVerifies(t *testing.T) {
	ca := newTestCA(t)
	dir := t.TempDir()
	ca.issue(t, dir, 10)
	r, err := newCertReloader(filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key"), time.Second)
	require.NoError(t, err)
	addr := startTLSServer(t, r)

	// A client that trusts the CA connects and gets an answer.
	require.NoError(t, pingTLS(addr, &tls.Config{RootCAs: ca.pool}))

	// A client that uses other roots does not accept the server.
	require.Error(t, pingTLS(addr, &tls.Config{RootCAs: x509.NewCertPool()}))

	// A plaintext client gets no answer.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	plain := &api.StreamTransport{Addr: addr}
	defer plain.Close()
	require.Error(t, plain.Ping(ctx))
}

func TestTLSReloadsSwappedSecret(t *testing.T) {
	ca := newTestCA(t)
	mount := t.TempDir()
	ca.issue(t, filepath.Join(mount, "v1"), 11)
	mountSecret(t, mount, "v1")

	clock := time.Now()
	r, err := newCertReloader(filepath.Join(mount, "tls.crt"), filepath.Join(mount, "tls.key"), time.Minute)
	require.NoError(t, err)
	r.now = func() time.Time { return clock }
	addr := startTLSServer(t, r)
	require.Equal(t, int64(11), servedSerial(t, addr, ca.pool))

	// The kubelet swaps the Secret. Before the interval ends, the old
	// certificate stays in use.
	ca.issue(t, filepath.Join(mount, "v2"), 12)
	mountSecret(t, mount, "v2")
	require.Equal(t, int64(11), servedSerial(t, addr, ca.pool))

	// After the interval, the next handshake loads the new certificate.
	clock = clock.Add(2 * time.Minute)
	require.Equal(t, int64(12), servedSerial(t, addr, ca.pool))
	require.NoError(t, pingTLS(addr, &tls.Config{RootCAs: ca.pool}))

	// A bad update (a key that does not match) keeps the last good certificate.
	ca.issue(t, filepath.Join(mount, "v3"), 13)
	other := t.TempDir()
	ca.issue(t, other, 14)
	key, err := os.ReadFile(filepath.Join(other, "tls.key"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(mount, "v3", "tls.key"), key, 0o600))
	mountSecret(t, mount, "v3")
	clock = clock.Add(2 * time.Minute)
	require.Equal(t, int64(12), servedSerial(t, addr, ca.pool))
}

func TestRPCTLSConfigNeedsBothFiles(t *testing.T) {
	origCert, origKey := tlsCertFile, tlsKeyFile
	defer func() { tlsCertFile, tlsKeyFile = origCert, origKey }()

	tlsCertFile, tlsKeyFile = "", ""
	cfg, err := rpcTLSConfig()
	require.NoError(t, err)
	require.Nil(t, cfg)

	tlsCertFile, tlsKeyFile = "/nonexistent/tls.crt", ""
	_, err = rpcTLSConfig()
	require.Error(t, err)
}
