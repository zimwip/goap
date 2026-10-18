package platform

import (
	"log/slog"
	"net/http"
	"net/http/pprof"
	"time"
)

// servePprof serves the Go profiles (net/http/pprof) on GOAP_PPROF_ADDR when it is set, on a listener of their own
// (never on the service's port): the performance stack (make perf) profiles the graph service under load with it.
func servePprof(log *slog.Logger) {
	addr := Env("GOAP_PPROF_ADDR", "")
	if addr == "" {
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Info("pprof", "addr", addr)
		if err := srv.ListenAndServe(); err != nil {
			log.Error("pprof", "err", err)
		}
	}()
}
