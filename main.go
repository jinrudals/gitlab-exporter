package main

import (
	// "fmt"
	// "github.com/prometheus/client_golang/prometheus"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sort"

	"net/http"
	"runtime"

	"github.com/alecthomas/kingpin/v2"
	"gopkg.in/ini.v1"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/prometheus/exporter-toolkit/web"
	"github.com/prometheus/exporter-toolkit/web/kingpinflag"

	"gitlab-exporter/collector"

	"github.com/prometheus/common/promslog"
	"github.com/prometheus/common/promslog/flag"
	"github.com/prometheus/common/version"

	bosgitlab "gitlab-exporter/gitlab"
)

type handler struct {
	unfilteredHandler       http.Handler
	enabledCollectors       []string
	exporterMetricsRegistry *prometheus.Registry
	logger                  *slog.Logger
}

func newHandler(logger *slog.Logger) *handler {
	h := &handler{
		exporterMetricsRegistry: prometheus.NewRegistry(),
		logger:                  logger,
	}
	if innerHandler, err := h.innerHandler(); err != nil {
		panic(fmt.Sprintf("Couldn't create metrics handler: %s", err))
	} else {
		h.unfilteredHandler = innerHandler
	}
	return h
}
func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	collects := r.URL.Query()["collect[]"]
	h.logger.Debug("collect query:", "collects", collects)

	excludes := r.URL.Query()["exclude[]"]
	h.logger.Debug("exclude query:", "excludes", excludes)

	if len(collects) == 0 && len(excludes) == 0 {
		h.unfilteredHandler.ServeHTTP(w, r)
		return
	}

	if len(collects) > 0 && len(excludes) > 0 {
		h.logger.Debug("rejecting combined collect and exclude queries")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Combined collect and exclude queries are not allowed"))
		return
	}

	filters := &collects

	if len(excludes) > 0 {
		f := []string{}
		for _, c := range h.enabledCollectors {
			if (slices.Index(excludes, c)) == -1 {
				f = append(f, c)
			}
		}
		filters = &f
	}

	filteredHandler, err := h.innerHandler(*filters...)
	if err != nil {
		h.logger.Warn("Couldn't create filtered metrics handler:", "err", err)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(fmt.Sprintf("Couldn't created filtered metrics handler : %s", err)))
		return
	}

	filteredHandler.ServeHTTP(w, r)
}
func (h *handler) innerHandler(filters ...string) (http.Handler, error) {
	nc, err := collector.NewGitLabCollector(h.logger, filters...)

	if err != nil {
		return nil, fmt.Errorf("couldn't created collector : %s", err)
	}

	if len(filters) == 0 {
		h.logger.Info("Enabled collectors")
		for n := range nc.Collectors {
			h.enabledCollectors = append(h.enabledCollectors, n)
		}
		sort.Strings(h.enabledCollectors)
		for _, c := range h.enabledCollectors {
			h.logger.Info(c)
		}
	}
	r := prometheus.NewRegistry()

	if err := r.Register(nc); err != nil {
		return nil, fmt.Errorf("couldn't register node collector: %s", err)
	}
	var handler http.Handler

	handler = promhttp.HandlerFor(
		r,
		promhttp.HandlerOpts{
			ErrorLog:            slog.NewLogLogger(h.logger.Handler(), slog.LevelError),
			ErrorHandling:       promhttp.ContinueOnError,
			MaxRequestsInFlight: 1,
		},
	)
	return handler, nil
}
func main() {
	maxProcs := kingpin.Flag(
		"runtime.gomaxprocs", "The target number of CPUs Go will run on (GOMAXPROCS)",
	).Envar("GOMAXPROCS").Default("1").Int()
	metricsPath := kingpin.Flag(
		"web.telemetry-path",
		"Path under which to expose metrics.",
	).Default("/metrics").String()
	configFile := kingpin.Flag(
		"gitlab.config-path",
		"GitLab config path",
	).Default("./config.ini").String()
	toolkitFlags := kingpinflag.AddFlags(kingpin.CommandLine, ":9100")
	promslogConfig := &promslog.Config{}
	flag.AddFlags(kingpin.CommandLine, promslogConfig)

	kingpin.Parse()

	logger := promslog.New(promslogConfig)
	runtime.GOMAXPROCS(*maxProcs)

	cfg, err1 := ini.Load(*configFile)

	if err1 != nil {
		logger.Error(fmt.Sprintf("Failed to load config file: %v", err1))
	}
	token := cfg.Section("gitlab").Key("TOKEN").String()
	url := cfg.Section("gitlab").Key("URL").String()

	bosgitlab.Initialize(token, url)
	bosgitlab.GetClient()

	http.Handle(*metricsPath, newHandler(logger))
	if *metricsPath != "/" {
		landingConfig := web.LandingConfig{
			Name:        "GitLab Exporter",
			Description: "Prometheus GitLab Exporter",
			Version:     version.Info(),
			Links: []web.LandingLinks{
				{
					Address: *metricsPath,
					Text:    "Metrics",
				},
			},
		}
		landingPage, err := web.NewLandingPage(landingConfig)
		if err != nil {
			logger.Error(err.Error())
			os.Exit(1)
		}
		http.Handle("/", landingPage)
	}

	server := &http.Server{}
	if err := web.ListenAndServe(server, toolkitFlags, logger); err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}
}
