package edge

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// CA lifetimes. A run lives at most harness.idle_timeout past its last call,
// so a day covers it; a leaf never outlives the CA.
const (
	CALifetime = 24 * time.Hour
	// caBackdate covers a container clock a little behind the edge's.
	caBackdate = 5 * time.Minute
	// CACertName is the certificate's file in the CA directory the app
	// mounts read-only.
	CACertName = "ca.pem"
	// maxLeaves bounds the leaf cache; past it the cache starts over.
	maxLeaves = 1024
)

// CA is a run's sandbox certificate authority. The edge makes it at start and
// it lives only in the edge's memory: no method returns or writes the private
// key, so it ends with the edge process.
type CA struct {
	cert    *x509.Certificate
	certPEM []byte
	key     *ecdsa.PrivateKey
	// leafKey signs every leaf; one key keeps a handshake cheap.
	leafKey *ecdsa.PrivateKey

	mu     sync.Mutex
	leaves map[string]*tls.Certificate
}

// NewCA makes a P-256 CA valid for CALifetime from now.
func NewCA(now time.Time) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("ca key: %w", err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("leaf key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	pub, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, err
	}
	ski := sha256.Sum256(pub)
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "aiman harness run CA", Organization: []string{"aiman harness sandbox"}},
		NotBefore:             now.Add(-caBackdate),
		NotAfter:              now.Add(CALifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
		SubjectKeyId:          ski[:20],
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("ca certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &CA{
		cert:    cert,
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		key:     key,
		leafKey: leafKey,
		leaves:  map[string]*tls.Certificate{},
	}, nil
}

func randomSerial() (*big.Int, error) {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, fmt.Errorf("serial: %w", err)
	}
	return n.Add(n, big.NewInt(1)), nil
}

// CertPEM is the CA certificate, PEM encoded. It holds no key.
func (c *CA) CertPEM() []byte { return append([]byte(nil), c.certPEM...) }

// Certificate is the parsed CA certificate.
func (c *CA) Certificate() *x509.Certificate { return c.cert }

// WriteCert writes the certificate, and only the certificate, to
// dir/ca.pem, readable by the app's user.
func (c *CA) WriteCert(dir string) error {
	path := filepath.Join(dir, CACertName)
	tmp, err := os.CreateTemp(dir, ".ca-*.pem")
	if err != nil {
		return fmt.Errorf("write ca certificate: %w", err)
	}
	_, werr := tmp.Write(c.certPEM)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("write ca certificate: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil { //nolint:gosec // G302: a public certificate the app reads
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Leaf returns a server certificate for host (a DNS name or an IP), signed
// by the CA and cached per host.
func (c *CA) Leaf(host string) (*tls.Certificate, error) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" {
		return nil, errors.New("leaf certificate needs a host name")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if l, ok := c.leaves[host]; ok {
		return l, nil
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    now.Add(-caBackdate),
		NotAfter:     c.cert.NotAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &c.leafKey.PublicKey, c.key)
	if err != nil {
		return nil, fmt.Errorf("leaf certificate for %s: %w", host, err)
	}
	leaf := &tls.Certificate{Certificate: [][]byte{der, c.cert.Raw}, PrivateKey: c.leafKey}
	if len(c.leaves) >= maxLeaves {
		c.leaves = map[string]*tls.Certificate{}
	}
	c.leaves[host] = leaf
	return leaf, nil
}
