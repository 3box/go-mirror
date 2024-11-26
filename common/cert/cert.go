package cert

import (
	"crypto/tls"
	"net/http"
)

type CertManager interface {
	GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error)
	GetHTTPHandler() http.Handler
}
