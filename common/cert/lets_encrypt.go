package cert

import (
	"crypto/tls"
	"net/http"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"

	"github.com/3box/go-mirror/common/config"
	"github.com/3box/go-mirror/common/logging"
)

type acmeCertManager struct {
	logger     logging.Logger
	certConfig *config.CertConfig
	manager    *autocert.Manager
}

func NewACMECertManager(cfg *config.Config, logger logging.Logger) (CertManager, error) {
	certConfig := cfg.Cert
	if !certConfig.Enabled {
		return nil, nil
	}

	manager := &autocert.Manager{
		Cache:      autocert.DirCache(certConfig.CacheDir),
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(certConfig.Domains...),
	}

	if certConfig.TestMode {
		manager.Client = &acme.Client{
			DirectoryURL: "https://acme-staging-v02.api.letsencrypt.org/directory",
		}
	}

	return &acmeCertManager{
		logger:     logger,
		certConfig: &certConfig,
		manager:    manager,
	}, nil
}

func (_this *acmeCertManager) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	return _this.manager.GetCertificate(hello)
}

func (_this *acmeCertManager) GetHTTPHandler() http.Handler {
	return _this.manager.HTTPHandler(nil)
}
