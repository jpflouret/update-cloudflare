package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/cloudflare/cloudflare-go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
)

var (
	updateDuration = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "update_cloudflare_duration_total",
		Help: "Duration for updating Cloudflare DNS",
	})

	updatesTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "update_cloudflare_updates_total",
		Help: "Total number of Cloudflare DNS record updates performed",
	})
)

func init() {
	prometheus.MustRegister(updateDuration)
	prometheus.MustRegister(updatesTotal)
}

type appConfig struct {
	dnsName     string
	dnsTTL      int
	zoneID      string
	checkIPURL  string
	sleepPeriod time.Duration
	port        uint
	console     bool
}

type cloudflareAPI interface {
	ListDNSRecords(ctx context.Context, rc *cloudflare.ResourceContainer, params cloudflare.ListDNSRecordsParams) ([]cloudflare.DNSRecord, *cloudflare.ResultInfo, error)
	CreateDNSRecord(ctx context.Context, rc *cloudflare.ResourceContainer, params cloudflare.CreateDNSRecordParams) (cloudflare.DNSRecord, error)
	UpdateDNSRecord(ctx context.Context, rc *cloudflare.ResourceContainer, params cloudflare.UpdateDNSRecordParams) (cloudflare.DNSRecord, error)
}

type updater struct {
	cfg        appConfig
	log        zerolog.Logger
	cf         cloudflareAPI
	httpClient *http.Client
}

func parseConfig(args []string, getenv func(string) string) (appConfig, error) {
	fs := flag.NewFlagSet("update-cloudflare", flag.ContinueOnError)
	console := fs.Bool("console", false, "enable console logging")
	port := fs.Uint("port", 8080, "port for health check/metrics server")
	if err := fs.Parse(args); err != nil {
		return appConfig{}, err
	}

	cfg := appConfig{
		console:     *console,
		port:        *port,
		checkIPURL:  "http://checkip.amazonaws.com/",
		dnsTTL:      1, // Cloudflare "Auto" TTL
		sleepPeriod: 5 * time.Minute,
	}

	if cfg.port == 0 || cfg.port > 65535 {
		return appConfig{}, fmt.Errorf("invalid port number: %d", cfg.port)
	}

	cfg.dnsName = getenv("DNS_NAME")
	if cfg.dnsName == "" {
		return appConfig{}, fmt.Errorf("missing DNS_NAME environment variable")
	}

	cfg.zoneID = getenv("ZONE_ID")
	if cfg.zoneID == "" {
		return appConfig{}, fmt.Errorf("missing ZONE_ID environment variable")
	}

	if v := getenv("DNS_TTL"); v != "" {
		ttl, err := strconv.Atoi(v)
		if err != nil {
			return appConfig{}, fmt.Errorf("invalid DNS_TTL: %w", err)
		}
		cfg.dnsTTL = ttl
	}

	if v := getenv("CHECK_IP"); v != "" {
		if _, err := url.Parse(v); err != nil {
			return appConfig{}, fmt.Errorf("invalid CHECK_IP: %w", err)
		}
		cfg.checkIPURL = v
	}

	if v := getenv("SLEEP_PERIOD"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return appConfig{}, fmt.Errorf("invalid SLEEP_PERIOD: %w", err)
		}
		cfg.sleepPeriod = d
	}

	return cfg, nil
}

// currentRecord returns the record ID, current IP value, and TTL of the A record for dnsName.
// Returns empty strings and zero TTL if the record does not exist.
func (u *updater) currentRecord(ctx context.Context) (string, string, int, error) {
	rc := cloudflare.ZoneIdentifier(u.cfg.zoneID)
	params := cloudflare.ListDNSRecordsParams{
		Name: u.cfg.dnsName,
		Type: "A",
	}
	records, _, err := u.cf.ListDNSRecords(ctx, rc, params)
	if err != nil {
		return "", "", 0, fmt.Errorf("listing DNS records: %w", err)
	}
	for _, r := range records {
		if r.Name == u.cfg.dnsName && r.Type == "A" {
			return r.ID, r.Content, r.TTL, nil
		}
	}
	return "", "", 0, nil
}

func (u *updater) update(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.cfg.checkIPURL, nil)
	if err != nil {
		return fmt.Errorf("creating IP check request: %w", err)
	}
	resp, err := u.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetching current address: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response body: %w", err)
	}

	rawIP := strings.TrimSpace(string(body))
	if net.ParseIP(rawIP) == nil {
		return fmt.Errorf("invalid IP address: %q", rawIP)
	}

	recordID, currentValue, currentTTL, err := u.currentRecord(ctx)
	if err != nil {
		return fmt.Errorf("getting current record: %w", err)
	}

	if currentValue == rawIP && currentTTL == u.cfg.dnsTTL {
		u.log.Info().
			Str("currentAddress", rawIP).
			Str("currentRecordValue", currentValue).
			Int("currentRecordTTL", currentTTL).
			Msg("address has not changed")
		return nil
	}

	rc := cloudflare.ZoneIdentifier(u.cfg.zoneID)
	if recordID == "" {
		_, err = u.cf.CreateDNSRecord(ctx, rc, cloudflare.CreateDNSRecordParams{
			Name:    u.cfg.dnsName,
			Type:    "A",
			Content: rawIP,
			TTL:     u.cfg.dnsTTL,
		})
		if err != nil {
			return fmt.Errorf("creating DNS record: %w", err)
		}
	} else {
		_, err = u.cf.UpdateDNSRecord(ctx, rc, cloudflare.UpdateDNSRecordParams{
			ID:      recordID,
			Name:    u.cfg.dnsName,
			Type:    "A",
			Content: rawIP,
			TTL:     u.cfg.dnsTTL,
		})
		if err != nil {
			return fmt.Errorf("updating DNS record: %w", err)
		}
	}

	updatesTotal.Inc()
	u.log.Info().
		Str("currentAddress", rawIP).
		Str("dnsName", u.cfg.dnsName).
		Bool("created", recordID == "").
		Msg("DNS record updated")

	return nil
}

func run(ctx context.Context, args []string, getenv func(string) string) error {
	cfg, err := parseConfig(args, getenv)
	if err != nil {
		return err
	}

	var logger zerolog.Logger
	if cfg.console {
		logger = zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout}).With().Timestamp().Logger()
	} else {
		logger = zerolog.New(os.Stdout).With().Timestamp().Logger()
	}
	logger = logger.With().
		Str("dnsName", cfg.dnsName).
		Str("zoneID", cfg.zoneID).
		Logger()

	logger.Info().
		Str("checkIPURL", cfg.checkIPURL).
		Str("sleepPeriod", cfg.sleepPeriod.String()).
		Int("dnsTTL", cfg.dnsTTL).
		Msg("starting cloudflare-updater...")

	apiToken := getenv("CF_API_TOKEN")
	if apiToken == "" {
		return fmt.Errorf("missing CF_API_TOKEN environment variable")
	}

	cfAPI, err := cloudflare.NewWithAPIToken(apiToken)
	if err != nil {
		return fmt.Errorf("creating Cloudflare client: %w", err)
	}

	u := &updater{
		cfg:        cfg,
		log:        logger,
		cf:         cfAPI,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "200 OK")
	})
	mux.Handle("/metrics", promhttp.Handler())

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.port),
		Handler: mux,
	}

	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Err(err).Msg("server shutdown error")
		}
	}()

	serverErr := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	sleepTimer := time.NewTimer(0)
	defer sleepTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info().Msg("shutting down")
			return nil
		case err := <-serverErr:
			return fmt.Errorf("server failed: %w", err)
		case <-sleepTimer.C:
		}

		start := time.Now()
		if err := u.update(ctx); err != nil {
			logger.Err(err).Msg("update failed")
		}
		updateDuration.Add(time.Since(start).Seconds())

		sleepTimer.Reset(cfg.sleepPeriod)
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Getenv); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
}
