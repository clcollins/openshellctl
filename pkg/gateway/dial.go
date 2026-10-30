package gateway

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	types "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/openshift-online/openshellctl/pkg/auth"
	"github.com/openshift-online/openshellctl/pkg/gatewayconfig"
)

// Dial errors mirroring the SDK's connection rules (internal/grpc/conn.go).
var (
	ErrPlaintextWithTLSMaterial = errors.New("plaintext (http://) endpoint cannot use TLS material")
	ErrPlaintextWithAuth        = errors.New("plaintext (http://) endpoint cannot carry bearer auth")
	ErrRelativeTLSPath          = errors.New("TLS material paths must be absolute after root resolution")
)

// DialConfig configures a gateway connection.
type DialConfig struct {
	Endpoint string // http:// → plaintext; https:// or bare → TLS
	TLS      gatewayconfig.TLSMaterial
	// TLSRoot is the filesystem directory that TLS.CAFile/CertFile/KeyFile are
	// relative to (TLSMaterialFor returns fs-relative paths). Empty means the
	// paths are already absolute.
	TLSRoot   string
	Insecure  bool // --gateway-insecure → InsecureSkipVerify (TLS only)
	Auth      auth.TokenSource
	UserAgent string
}

// Conn holds the typed SDK client and the raw stub client, each over its own
// (lazy) grpc.ClientConn with identical credentials.
type Conn struct {
	SDK v1.ClientInterface
	Raw pb.OpenShellClient

	rawConn *grpc.ClientConn
}

// isPlaintext reports whether the endpoint selects a plaintext (h2c) transport.
func isPlaintext(endpoint string) bool {
	return strings.HasPrefix(endpoint, "http://")
}

// resolveTLSPaths resolves TLS material paths against TLSRoot once and
// validates that every non-empty path is absolute. Returns the resolved
// CA, cert, and key paths.
func resolveTLSPaths(cfg DialConfig) (ca, cert, key string, err error) {
	ca = underRoot(cfg.TLSRoot, cfg.TLS.CAFile)
	cert = underRoot(cfg.TLSRoot, cfg.TLS.CertFile)
	key = underRoot(cfg.TLSRoot, cfg.TLS.KeyFile)
	for _, p := range []string{ca, cert, key} {
		if p != "" && !filepath.IsAbs(p) {
			return "", "", "", fmt.Errorf("%w: %q", ErrRelativeTLSPath, p)
		}
	}
	return ca, cert, key, nil
}

// Dial builds both clients. It replicates the SDK's transport selection for the
// raw conn and delegates the SDK conn to v1.NewClient with the same material.
func Dial(cfg DialConfig) (*Conn, error) {
	plaintext := isPlaintext(cfg.Endpoint)

	perRPC := &perRPCCredentials{src: cfg.Auth}

	if plaintext {
		if cfg.TLS.Present {
			return nil, ErrPlaintextWithTLSMaterial
		}
		if perRPC.requiresTransportSecurity() {
			return nil, ErrPlaintextWithAuth
		}
	}

	// Resolve TLS paths once, before either conn is created.
	var caPath, certPath, keyPath string
	if cfg.TLS.Present {
		var err error
		caPath, certPath, keyPath, err = resolveTLSPaths(cfg)
		if err != nil {
			return nil, err
		}
	}

	// SDK conn.
	sdkCfg := types.Config{
		Address: cfg.Endpoint,
		Auth:    perRPC,
	}
	if cfg.TLS.Present {
		sdkCfg.TLS = &types.TLSConfig{
			CAFile:   caPath,
			CertFile: certPath,
			KeyFile:  keyPath,
			Insecure: cfg.Insecure,
		}
	} else if cfg.Insecure && !plaintext {
		sdkCfg.TLS = &types.TLSConfig{Insecure: true}
	}
	sdkClient, err := v1.NewClient(sdkCfg)
	if err != nil {
		return nil, fmt.Errorf("sdk client: %w", err)
	}

	// Raw conn, mirroring the SDK's transport rules.
	rawConn, err := dialRaw(cfg, plaintext, perRPC)
	if err != nil {
		_ = sdkClient.Close()
		return nil, err
	}

	return &Conn{
		SDK:     sdkClient,
		Raw:     pb.NewOpenShellClient(rawConn),
		rawConn: rawConn,
	}, nil
}

// dialRaw builds the second grpc.ClientConn used for the raw stub client.
func dialRaw(cfg DialConfig, plaintext bool, perRPC *perRPCCredentials) (*grpc.ClientConn, error) {
	target := stripScheme(cfg.Endpoint)

	opts := []grpc.DialOption{}
	if cfg.UserAgent != "" {
		opts = append(opts, grpc.WithUserAgent(cfg.UserAgent))
	}

	if plaintext {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	} else {
		tlsCfg, err := buildTLSConfig(cfg)
		if err != nil {
			return nil, err
		}
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
		if perRPC.hasAuth() {
			opts = append(opts, grpc.WithPerRPCCredentials(perRPC))
		}
	}

	return grpc.NewClient(target, opts...)
}

// buildTLSConfig assembles the client TLS config: CAFile → RootCAs, cert+key →
// client cert, Insecure → InsecureSkipVerify. MinVersion 1.2.
func buildTLSConfig(cfg DialConfig) (*tls.Config, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.Insecure {
		tlsCfg.InsecureSkipVerify = true
	}
	if !cfg.TLS.Present {
		return tlsCfg, nil
	}
	if ca := underRoot(cfg.TLSRoot, cfg.TLS.CAFile); ca != "" {
		pem, err := os.ReadFile(ca)
		if err != nil {
			return nil, fmt.Errorf("read CA %q: %w", ca, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificates parsed from CA %q", ca)
		}
		tlsCfg.RootCAs = pool
	}
	if cfg.TLS.HasClientCert() {
		cert, err := tls.LoadX509KeyPair(underRoot(cfg.TLSRoot, cfg.TLS.CertFile), underRoot(cfg.TLSRoot, cfg.TLS.KeyFile))
		if err != nil {
			return nil, fmt.Errorf("load client cert/key: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}
	return tlsCfg, nil
}

// underRoot resolves a TLSMaterial fs-relative path against root. TLSMaterialFor
// returns paths relative to the gateway sub-FS; the caller passes the concrete
// root via DialConfig.TLSRoot. An empty root or path is returned unchanged.
func underRoot(root, rel string) string {
	if rel == "" || root == "" {
		return rel
	}
	return filepath.Join(root, rel)
}

func stripScheme(endpoint string) string {
	endpoint = strings.TrimPrefix(endpoint, "https://")
	endpoint = strings.TrimPrefix(endpoint, "http://")
	return strings.TrimSuffix(endpoint, "/")
}

// Close closes the raw conn and the SDK client.
func (c *Conn) Close() error {
	if c == nil {
		return nil
	}
	var firstErr error
	if c.rawConn != nil {
		if err := c.rawConn.Close(); err != nil {
			firstErr = err
		}
	}
	if closer, ok := c.SDK.(interface{ Close() error }); ok {
		if err := closer.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
