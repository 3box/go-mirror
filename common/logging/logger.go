package logging

// Logger is the common interface for logging
type Logger interface {
	Debugf(template string, args ...interface{})
	Debugw(msg string, args ...interface{})
	Errorf(template string, args ...interface{})
	Errorw(msg string, args ...interface{})
	Fatalf(template string, args ...interface{})
	Infow(msg string, args ...interface{})
	Infof(template string, args ...interface{})
	Warnf(template string, args ...interface{})
	Warnw(msg string, args ...interface{})
	Sync() error
}
