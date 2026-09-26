package cmd

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/spf13/cobra"
)

// rpcFlags holds the connection flags that each client command shares.
type rpcFlags struct {
	address, port string
	useTLS        bool
	caFile        string
	serverName    string
}

func (f *rpcFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&f.address, "address", "a", "127.0.0.1", "RPC host name or IP address")
	cmd.Flags().StringVarP(&f.port, "port", "p", "5080", "RPC port")
	cmd.Flags().BoolVar(&f.useTLS, "tls", false, "Connect with TLS and verify the server against the system roots")
	cmd.Flags().StringVar(&f.caFile, "tls-ca-file", "", "Connect with TLS and verify the server against the CA certificates in this PEM file")
	cmd.Flags().StringVar(&f.serverName, "tls-server-name", "", "Name to verify in the server certificate (default: the address)")
}

func (f *rpcFlags) client() (*api.CorndogsClient, error) {
	addr := net.JoinHostPort(f.address, f.port)
	if !f.useTLS && f.caFile == "" {
		if f.serverName != "" {
			return nil, fmt.Errorf("--tls-server-name needs --tls or --tls-ca-file")
		}
		return api.New(addr), nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: f.serverName}
	if f.caFile != "" {
		pem, err := os.ReadFile(f.caFile)
		if err != nil {
			return nil, fmt.Errorf("read --tls-ca-file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("--tls-ca-file %s contains no PEM certificates", f.caFile)
		}
		cfg.RootCAs = pool
	}
	return api.NewTLS(addr, cfg), nil
}
