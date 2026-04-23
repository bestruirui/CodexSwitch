package shutdown

import (
	"os"
	"os/signal"
	"syscall"
)

type logger interface {
	Infof(template string, args ...interface{})
	Errorf(template string, args ...interface{})
	Warnf(template string, args ...interface{})
	Debugf(template string, args ...interface{})
}

type ShutDown struct {
	log   logger
	funcs []func() error
}

func New(log logger) *ShutDown {
	return &ShutDown{log: log}
}

func (s *ShutDown) Register(fn func() error) {
	s.funcs = append(s.funcs, fn)
}

func (s *ShutDown) Listen() {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	s.log.Infof("Program started, press Ctrl+C to exit")
	sig := <-quit
	s.log.Warnf("Received exit signal: %v", sig)
	if len(s.funcs) == 0 {
		return
	}
	for _, fn := range s.funcs {
		if err := fn(); err != nil {
			s.log.Errorf("Closing functions execution failed: %v", err)
		}
	}
	s.log.Infof("Shutdown completed successfully")
	os.Exit(0)
}
