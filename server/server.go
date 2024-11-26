package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/3box/go-mirror/common/cert"
	"github.com/3box/go-mirror/common/config"
	"github.com/3box/go-mirror/common/logging"
	"github.com/3box/go-mirror/common/metric"
	"github.com/3box/go-mirror/controllers"
)

type Server interface {
	Run()
}

// serverImpl is the struct that implements the server interface. It pulls together the individual implementations for
// the different controllers.
type serverImpl struct {
	ctx             context.Context
	cfg             *config.Config
	logger          logging.Logger
	proxyServer     *http.Server
	challengeServer *http.Server
	proxyController controllers.ProxyController
	metrics         metric.MetricService
	certManager     cert.CertManager
}

func NewServer(
	ctx context.Context,
	cfg *config.Config,
	logger logging.Logger,
	metrics metric.MetricService,
	proxyController controllers.ProxyController,
	certManager cert.CertManager,
) (*gin.Engine, Server) {
	router := gin.New()

	server := &serverImpl{
		ctx:    ctx,
		cfg:    cfg,
		logger: logger,
		proxyServer: &http.Server{
			Handler: router,
			Addr:    cfg.Proxy.ListenAddr,
		},
		proxyController: proxyController,
		metrics:         metrics,
	}

	if cfg.Proxy.TLSEnabled && certManager != nil {
		server.proxyServer.TLSConfig = &tls.Config{
			GetCertificate: certManager.GetCertificate,
			MinVersion:     tls.VersionTLS12,
			MaxVersion:     tls.VersionTLS13,
		}

		// HTTP server for ACME challenges
		server.challengeServer = &http.Server{
			Addr:    cfg.Cert.ListenAddr,
			Handler: certManager.GetHTTPHandler(),
		}
	}

	// Match all paths including root
	router.Any("/*path", server.handleProxy)

	return router, server
}

func (_this serverImpl) handleProxy(c *gin.Context) {
	if strings.HasPrefix(c.Request.URL.Path, "/.well-known/acme-challenge/") {
		_this.certManager.GetHTTPHandler().ServeHTTP(c.Writer, c.Request)
		return
	} else if strings.HasPrefix(c.Request.URL.Path, "/metrics") {
		_this.metrics.GetPrometheusHandler()(c)
		return
	}

	switch c.Request.Method {
	case http.MethodGet:
		_this.proxyController.ProxyGetRequest(c)
	case http.MethodPost:
		_this.proxyController.ProxyPostRequest(c)
	case http.MethodPut:
		_this.proxyController.ProxyPutRequest(c)
	case http.MethodDelete:
		_this.proxyController.ProxyDeleteRequest(c)
	default:
		c.Status(http.StatusMethodNotAllowed)
	}
}

func (_this serverImpl) Run() {
	// Set up a server context
	serverCtx, serverCtxCancel := context.WithCancel(_this.ctx)
	_this.proxyServer.BaseContext = func(net.Listener) context.Context {
		return serverCtx
	}

	wg := sync.WaitGroup{}
	wg.Add(3)

	// Start the proxy server
	go func() {
		defer wg.Done()

		var err error
		if _this.cfg.Cert.Enabled {
			_this.logger.Infof("server: proxy server starting with ACME on %s", _this.proxyServer.Addr)
			err = _this.proxyServer.ListenAndServeTLS("", "") // Empty strings trigger cert manager
		} else if _this.cfg.Cert.TestMode {
			// Generate a self-signed certificate for testing
			crt, err := generateSelfSignedCert()
			if err != nil {
				_this.logger.Fatalf("failed to generate certificate: %v", err)
			}
			_this.logger.Infof("server: proxy server starting with self-signed cert on %s", _this.proxyServer.Addr)
			err = _this.proxyServer.ListenAndServeTLS(crt.CertFile, crt.KeyFile)
		} else {
			// No TLS
			_this.logger.Infof("server: proxy server starting on %s", _this.proxyServer.Addr)
			err = _this.proxyServer.ListenAndServe()
		}

		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			_this.logger.Fatalf("proxy server listen error: %s", err)
		}
	}()

	// Start the server for ACME challenges
	go func() {
		defer wg.Done()

		if _this.cfg.Cert.Enabled {
			_this.logger.Infof("server: challenge server starting on %s", _this.challengeServer.Addr)
			if err := _this.challengeServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				_this.logger.Errorw("challenge server listen error", "error", err)
			}
		}
	}()

	// Graceful shutdown
	go func() {
		defer wg.Done()

		// Wait for interrupt signal to gracefully shutdown the server
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGHUP)
		<-quit

		serverCtxCancel()
		_this.logger.Infof("server: shutdown started...")

		// Let shutdown take as long the parent context allows
		if err := _this.Stop(); err != nil {
			_this.logger.Fatalf("server: shutdown error: %s", err)
		}

		_this.logger.Infof("server: shutdown complete")
		_ = _this.logger.Sync()
	}()

	// Wait for both goroutines to finish
	wg.Wait()
}

func generateSelfSignedCert() (*struct{ CertFile, KeyFile string }, error) {
	dir, err := os.MkdirTemp("", "cert")
	if err != nil {
		return nil, err
	}

	certFile := filepath.Join(dir, "cert.pem")
	keyFile := filepath.Join(dir, "key.pem")

	// Generate private key
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}

	// Create certificate template
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"Test Organization"},
		},
		NotBefore: time.Now(),
		NotAfter:  time.Now().Add(time.Hour * 24 * 180), // 180 days
		KeyUsage:  x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
		},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("100.66.104.46")},
	}

	// Create certificate
	derBytes, err := x509.CreateCertificate(
		rand.Reader,
		&template,
		&template,
		&privateKey.PublicKey,
		privateKey,
	)
	if err != nil {
		return nil, err
	}

	// Write certificate to file
	certOut, err := os.Create(certFile)
	if err != nil {
		return nil, err
	}
	defer certOut.Close()
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes}); err != nil {
		return nil, err
	}

	// Write private key to file
	keyOut, err := os.Create(keyFile)
	if err != nil {
		return nil, err
	}
	defer keyOut.Close()
	if err := pem.Encode(keyOut, &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	}); err != nil {
		return nil, err
	}

	return &struct{ CertFile, KeyFile string }{
		CertFile: certFile,
		KeyFile:  keyFile,
	}, nil
}

func (_this serverImpl) Stop() error {
	ctx, cancel := context.WithTimeout(_this.ctx, 5*time.Second)
	defer cancel()

	var errs []error

	if _this.challengeServer != nil {
		if err := _this.challengeServer.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("challenge server shutdown error: %w", err))
		}
	}

	if err := _this.proxyServer.Shutdown(ctx); err != nil {
		errs = append(errs, fmt.Errorf("proxy server shutdown error: %w", err))
	}

	if len(errs) > 0 {
		return fmt.Errorf("shutdown errors: %v", errs)
	}

	return nil
}
