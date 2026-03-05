package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cloudflare/cloudflare-go"
	"github.com/rs/zerolog"
)

// --- Mocks ---

type mockCloudflare struct {
	listFn   func(ctx context.Context, rc *cloudflare.ResourceContainer, params cloudflare.ListDNSRecordsParams) ([]cloudflare.DNSRecord, *cloudflare.ResultInfo, error)
	createFn func(ctx context.Context, rc *cloudflare.ResourceContainer, params cloudflare.CreateDNSRecordParams) (cloudflare.DNSRecord, error)
	updateFn func(ctx context.Context, rc *cloudflare.ResourceContainer, params cloudflare.UpdateDNSRecordParams) (cloudflare.DNSRecord, error)
}

func (m *mockCloudflare) ListDNSRecords(ctx context.Context, rc *cloudflare.ResourceContainer, params cloudflare.ListDNSRecordsParams) ([]cloudflare.DNSRecord, *cloudflare.ResultInfo, error) {
	return m.listFn(ctx, rc, params)
}

func (m *mockCloudflare) CreateDNSRecord(ctx context.Context, rc *cloudflare.ResourceContainer, params cloudflare.CreateDNSRecordParams) (cloudflare.DNSRecord, error) {
	if m.createFn != nil {
		return m.createFn(ctx, rc, params)
	}
	return cloudflare.DNSRecord{}, nil
}

func (m *mockCloudflare) UpdateDNSRecord(ctx context.Context, rc *cloudflare.ResourceContainer, params cloudflare.UpdateDNSRecordParams) (cloudflare.DNSRecord, error) {
	if m.updateFn != nil {
		return m.updateFn(ctx, rc, params)
	}
	return cloudflare.DNSRecord{}, nil
}

// --- Helpers ---

func newTestUpdater(cf *mockCloudflare, ipServer *httptest.Server) *updater {
	return &updater{
		cfg: appConfig{
			dnsName:    "home.example.com",
			dnsTTL:     300,
			zoneID:     "zone123",
			checkIPURL: ipServer.URL,
		},
		log:        zerolog.Nop(),
		cf:         cf,
		httpClient: ipServer.Client(),
	}
}

func makeEnv(env map[string]string) func(string) string {
	return func(key string) string { return env[key] }
}

// --- Tests ---

func TestParseConfig(t *testing.T) {
	baseEnv := map[string]string{
		"DNS_NAME": "home.example.com",
		"ZONE_ID":  "zone123",
	}

	t.Run("valid defaults", func(t *testing.T) {
		cfg, err := parseConfig(nil, makeEnv(baseEnv))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.dnsName != "home.example.com" {
			t.Errorf("dnsName = %q, want %q", cfg.dnsName, "home.example.com")
		}
		if cfg.zoneID != "zone123" {
			t.Errorf("zoneID = %q, want %q", cfg.zoneID, "zone123")
		}
		if cfg.dnsTTL != 1 {
			t.Errorf("dnsTTL = %d, want 1", cfg.dnsTTL)
		}
		if cfg.port != 8080 {
			t.Errorf("port = %d, want 8080", cfg.port)
		}
		if cfg.sleepPeriod != 5*time.Minute {
			t.Errorf("sleepPeriod = %v, want 5m", cfg.sleepPeriod)
		}
		if cfg.checkIPURL != "http://checkip.amazonaws.com/" {
			t.Errorf("checkIPURL = %q, want default", cfg.checkIPURL)
		}
	})

	t.Run("custom values", func(t *testing.T) {
		env := map[string]string{
			"DNS_NAME":     "home.example.com",
			"ZONE_ID":      "zone123",
			"DNS_TTL":      "60",
			"CHECK_IP":     "http://myip.example.com/",
			"SLEEP_PERIOD": "10m",
		}
		cfg, err := parseConfig([]string{"-port", "9090", "-console"}, makeEnv(env))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.dnsTTL != 60 {
			t.Errorf("dnsTTL = %d, want 60", cfg.dnsTTL)
		}
		if cfg.port != 9090 {
			t.Errorf("port = %d, want 9090", cfg.port)
		}
		if !cfg.console {
			t.Error("console = false, want true")
		}
		if cfg.checkIPURL != "http://myip.example.com/" {
			t.Errorf("checkIPURL = %q, want custom", cfg.checkIPURL)
		}
		if cfg.sleepPeriod != 10*time.Minute {
			t.Errorf("sleepPeriod = %v, want 10m", cfg.sleepPeriod)
		}
	})

	t.Run("missing DNS_NAME", func(t *testing.T) {
		_, err := parseConfig(nil, makeEnv(map[string]string{"ZONE_ID": "zone123"}))
		if err == nil {
			t.Fatal("expected error for missing DNS_NAME")
		}
	})

	t.Run("missing ZONE_ID", func(t *testing.T) {
		_, err := parseConfig(nil, makeEnv(map[string]string{"DNS_NAME": "example.com"}))
		if err == nil {
			t.Fatal("expected error for missing ZONE_ID")
		}
	})

	t.Run("invalid DNS_TTL", func(t *testing.T) {
		env := map[string]string{
			"DNS_NAME": "example.com",
			"ZONE_ID":  "zone123",
			"DNS_TTL":  "abc",
		}
		_, err := parseConfig(nil, makeEnv(env))
		if err == nil {
			t.Fatal("expected error for invalid DNS_TTL")
		}
	})

	t.Run("invalid SLEEP_PERIOD", func(t *testing.T) {
		env := map[string]string{
			"DNS_NAME":     "example.com",
			"ZONE_ID":      "zone123",
			"SLEEP_PERIOD": "not-a-duration",
		}
		_, err := parseConfig(nil, makeEnv(env))
		if err == nil {
			t.Fatal("expected error for invalid SLEEP_PERIOD")
		}
	})

	t.Run("invalid port zero", func(t *testing.T) {
		_, err := parseConfig([]string{"-port", "0"}, makeEnv(baseEnv))
		if err == nil {
			t.Fatal("expected error for port 0")
		}
	})
}

func TestCurrentRecord(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		mock := &mockCloudflare{
			listFn: func(_ context.Context, _ *cloudflare.ResourceContainer, _ cloudflare.ListDNSRecordsParams) ([]cloudflare.DNSRecord, *cloudflare.ResultInfo, error) {
				return []cloudflare.DNSRecord{
					{ID: "rec1", Name: "home.example.com", Type: "A", Content: "1.2.3.4", TTL: 300},
				}, &cloudflare.ResultInfo{}, nil
			},
		}
		u := &updater{
			cfg: appConfig{dnsName: "home.example.com", zoneID: "zone123"},
			cf:  mock,
		}
		id, value, ttl, err := u.currentRecord(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if id != "rec1" {
			t.Errorf("id = %q, want %q", id, "rec1")
		}
		if value != "1.2.3.4" {
			t.Errorf("value = %q, want %q", value, "1.2.3.4")
		}
		if ttl != 300 {
			t.Errorf("ttl = %d, want 300", ttl)
		}
	})

	t.Run("not found", func(t *testing.T) {
		mock := &mockCloudflare{
			listFn: func(_ context.Context, _ *cloudflare.ResourceContainer, _ cloudflare.ListDNSRecordsParams) ([]cloudflare.DNSRecord, *cloudflare.ResultInfo, error) {
				return []cloudflare.DNSRecord{}, &cloudflare.ResultInfo{}, nil
			},
		}
		u := &updater{
			cfg: appConfig{dnsName: "home.example.com", zoneID: "zone123"},
			cf:  mock,
		}
		id, value, ttl, err := u.currentRecord(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if id != "" || value != "" || ttl != 0 {
			t.Errorf("expected empty result, got id=%q value=%q ttl=%d", id, value, ttl)
		}
	})

	t.Run("API error", func(t *testing.T) {
		mock := &mockCloudflare{
			listFn: func(_ context.Context, _ *cloudflare.ResourceContainer, _ cloudflare.ListDNSRecordsParams) ([]cloudflare.DNSRecord, *cloudflare.ResultInfo, error) {
				return nil, nil, fmt.Errorf("access denied")
			},
		}
		u := &updater{
			cfg: appConfig{dnsName: "home.example.com", zoneID: "zone123"},
			cf:  mock,
		}
		_, _, _, err := u.currentRecord(context.Background())
		if err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestUpdate(t *testing.T) {
	t.Run("no change needed", func(t *testing.T) {
		ipServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "1.2.3.4\n")
		}))
		defer ipServer.Close()

		updateCalled := false
		createCalled := false
		mock := &mockCloudflare{
			listFn: func(_ context.Context, _ *cloudflare.ResourceContainer, _ cloudflare.ListDNSRecordsParams) ([]cloudflare.DNSRecord, *cloudflare.ResultInfo, error) {
				return []cloudflare.DNSRecord{
					{ID: "rec1", Name: "home.example.com", Type: "A", Content: "1.2.3.4", TTL: 300},
				}, &cloudflare.ResultInfo{}, nil
			},
			updateFn: func(_ context.Context, _ *cloudflare.ResourceContainer, _ cloudflare.UpdateDNSRecordParams) (cloudflare.DNSRecord, error) {
				updateCalled = true
				return cloudflare.DNSRecord{}, nil
			},
			createFn: func(_ context.Context, _ *cloudflare.ResourceContainer, _ cloudflare.CreateDNSRecordParams) (cloudflare.DNSRecord, error) {
				createCalled = true
				return cloudflare.DNSRecord{}, nil
			},
		}

		u := newTestUpdater(mock, ipServer)
		if err := u.update(context.Background()); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if updateCalled || createCalled {
			t.Error("update/create should not have been called")
		}
	})

	t.Run("IP changed, record exists", func(t *testing.T) {
		ipServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "5.6.7.8\n")
		}))
		defer ipServer.Close()

		var gotUpdateParams cloudflare.UpdateDNSRecordParams
		mock := &mockCloudflare{
			listFn: func(_ context.Context, _ *cloudflare.ResourceContainer, _ cloudflare.ListDNSRecordsParams) ([]cloudflare.DNSRecord, *cloudflare.ResultInfo, error) {
				return []cloudflare.DNSRecord{
					{ID: "rec1", Name: "home.example.com", Type: "A", Content: "1.2.3.4", TTL: 300},
				}, &cloudflare.ResultInfo{}, nil
			},
			updateFn: func(_ context.Context, _ *cloudflare.ResourceContainer, params cloudflare.UpdateDNSRecordParams) (cloudflare.DNSRecord, error) {
				gotUpdateParams = params
				return cloudflare.DNSRecord{}, nil
			},
		}

		u := newTestUpdater(mock, ipServer)
		if err := u.update(context.Background()); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if gotUpdateParams.ID != "rec1" {
			t.Errorf("UpdateDNSRecord ID = %q, want %q", gotUpdateParams.ID, "rec1")
		}
		if gotUpdateParams.Content != "5.6.7.8" {
			t.Errorf("UpdateDNSRecord Content = %q, want %q", gotUpdateParams.Content, "5.6.7.8")
		}
		if gotUpdateParams.TTL != 300 {
			t.Errorf("UpdateDNSRecord TTL = %d, want 300", gotUpdateParams.TTL)
		}
	})

	t.Run("IP changed, no record", func(t *testing.T) {
		ipServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "5.6.7.8\n")
		}))
		defer ipServer.Close()

		var gotCreateParams cloudflare.CreateDNSRecordParams
		mock := &mockCloudflare{
			listFn: func(_ context.Context, _ *cloudflare.ResourceContainer, _ cloudflare.ListDNSRecordsParams) ([]cloudflare.DNSRecord, *cloudflare.ResultInfo, error) {
				return []cloudflare.DNSRecord{}, &cloudflare.ResultInfo{}, nil
			},
			createFn: func(_ context.Context, _ *cloudflare.ResourceContainer, params cloudflare.CreateDNSRecordParams) (cloudflare.DNSRecord, error) {
				gotCreateParams = params
				return cloudflare.DNSRecord{}, nil
			},
		}

		u := newTestUpdater(mock, ipServer)
		if err := u.update(context.Background()); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if gotCreateParams.Name != "home.example.com" {
			t.Errorf("CreateDNSRecord Name = %q, want %q", gotCreateParams.Name, "home.example.com")
		}
		if gotCreateParams.Content != "5.6.7.8" {
			t.Errorf("CreateDNSRecord Content = %q, want %q", gotCreateParams.Content, "5.6.7.8")
		}
		if gotCreateParams.Type != "A" {
			t.Errorf("CreateDNSRecord Type = %q, want A", gotCreateParams.Type)
		}
		if gotCreateParams.TTL != 300 {
			t.Errorf("CreateDNSRecord TTL = %d, want 300", gotCreateParams.TTL)
		}
	})

	t.Run("TTL changed", func(t *testing.T) {
		ipServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "1.2.3.4\n")
		}))
		defer ipServer.Close()

		var gotUpdateParams cloudflare.UpdateDNSRecordParams
		mock := &mockCloudflare{
			listFn: func(_ context.Context, _ *cloudflare.ResourceContainer, _ cloudflare.ListDNSRecordsParams) ([]cloudflare.DNSRecord, *cloudflare.ResultInfo, error) {
				return []cloudflare.DNSRecord{
					{ID: "rec1", Name: "home.example.com", Type: "A", Content: "1.2.3.4", TTL: 60},
				}, &cloudflare.ResultInfo{}, nil
			},
			updateFn: func(_ context.Context, _ *cloudflare.ResourceContainer, params cloudflare.UpdateDNSRecordParams) (cloudflare.DNSRecord, error) {
				gotUpdateParams = params
				return cloudflare.DNSRecord{}, nil
			},
		}

		u := newTestUpdater(mock, ipServer)
		if err := u.update(context.Background()); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if gotUpdateParams.TTL != 300 {
			t.Errorf("UpdateDNSRecord TTL = %d, want 300", gotUpdateParams.TTL)
		}
		if gotUpdateParams.Content != "1.2.3.4" {
			t.Errorf("UpdateDNSRecord Content = %q, want %q", gotUpdateParams.Content, "1.2.3.4")
		}
	})

	t.Run("invalid IP response", func(t *testing.T) {
		ipServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "not-an-ip")
		}))
		defer ipServer.Close()

		u := newTestUpdater(&mockCloudflare{}, ipServer)
		err := u.update(context.Background())
		if err == nil {
			t.Fatal("expected error for invalid IP")
		}
	})

	t.Run("Cloudflare list error", func(t *testing.T) {
		ipServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "1.2.3.4\n")
		}))
		defer ipServer.Close()

		mock := &mockCloudflare{
			listFn: func(_ context.Context, _ *cloudflare.ResourceContainer, _ cloudflare.ListDNSRecordsParams) ([]cloudflare.DNSRecord, *cloudflare.ResultInfo, error) {
				return nil, nil, fmt.Errorf("rate limited")
			},
		}

		u := newTestUpdater(mock, ipServer)
		err := u.update(context.Background())
		if err == nil {
			t.Fatal("expected error for Cloudflare list failure")
		}
	})
}

func TestHealthz(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "200 OK")
	})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Header().Get("Content-Type") != "text/plain" {
		t.Errorf("Content-Type = %q, want text/plain", rec.Header().Get("Content-Type"))
	}
	if rec.Body.String() != "200 OK" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "200 OK")
	}
}
