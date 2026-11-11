package hub

import (
	"crypto/tls"
	"log"
	"os"
	"sync"
	"time"
)

// CertReloader serves the certificate from disk and reads it again when the files change, so a renewed
// certificate takes effect without a restart. A renewal that cannot be read keeps the previous certificate.
type CertReloader struct {
	certPath, keyPath string
	log               *log.Logger

	mu      sync.Mutex
	cert    *tls.Certificate
	modCert time.Time
	modKey  time.Time
}

// NewCertReloader loads the certificate once; the hub does not start without one.
func NewCertReloader(certPath, keyPath string, logger *log.Logger) (*CertReloader, error) {
	r := &CertReloader{certPath: certPath, keyPath: keyPath, log: logger}
	if err := r.load(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *CertReloader) load() error {
	ci, err := os.Stat(r.certPath)
	if err != nil {
		return err
	}
	ki, err := os.Stat(r.keyPath)
	if err != nil {
		return err
	}
	cert, err := tls.LoadX509KeyPair(r.certPath, r.keyPath)
	if err != nil {
		return err
	}
	r.cert, r.modCert, r.modKey = &cert, ci.ModTime(), ki.ModTime()
	return nil
}

// GetCertificate is the tls.Config hook.
func (r *CertReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ci, errC := os.Stat(r.certPath)
	ki, errK := os.Stat(r.keyPath)
	if errC == nil && errK == nil && (!ci.ModTime().Equal(r.modCert) || !ki.ModTime().Equal(r.modKey)) {
		old := r.cert
		if err := r.load(); err != nil {
			r.cert = old
			r.log.Printf("tls: renewed certificate not readable, keeping the previous one: %v", err)
		} else {
			r.log.Printf("tls: certificate reloaded")
		}
	}
	return r.cert, nil
}
