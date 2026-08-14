package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/kresd-manager/backend/internal/config"
)

type Server struct {
	cfg            *config.Config
	promURL        string
	ch             driver.Conn
	pg             *pgxpool.Pool
	rdb            *redis.Client
	httpClient     *http.Client
	clusterRole    atomic.Value // stores current role string
	pollerCancel      context.CancelFunc
	maintenanceMu     sync.Mutex // shared lock: prevents RPZ sync and system update from running simultaneously
	localUpdateCache  atomic.Value // stores json.RawMessage of local update check
}

func NewRouter(cfg *config.Config) (http.Handler, func(), error) {
	// ClickHouse connection
	chHost := strings.TrimPrefix(cfg.ClickHouseURL, "http://")
	chHost = strings.Replace(chHost, "8123", "9000", 1)

	ch, err := clickhouse.Open(&clickhouse.Options{
		Addr:            []string{chHost},
		Auth:            clickhouse.Auth{Database: cfg.ClickHouseDB, Username: "default", Password: ""},
		DialTimeout:     5 * time.Second,
		MaxOpenConns:    10,
		MaxIdleConns:    5,
		ConnMaxLifetime: 10 * time.Minute,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("clickhouse: %w", err)
	}

	// PostgreSQL connection pool
	pg, err := pgxpool.New(context.Background(), cfg.PostgresURL)
	if err != nil {
		return nil, nil, fmt.Errorf("postgres: %w", err)
	}

	// Initialize PostgreSQL schema
	if err := initPostgres(pg); err != nil {
		return nil, nil, fmt.Errorf("postgres init: %w", err)
	}

	// Redis client
	opt, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return nil, nil, fmt.Errorf("redis parse url: %w", err)
	}
	rdb := redis.NewClient(opt)

	srv := &Server{
		cfg:     cfg,
		promURL: cfg.PrometheusURL,
		ch:      ch,
		pg:      pg,
		rdb:     rdb,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}

	// Initialize cluster role from DB (or seed from env)
	srv.initClusterRole()

	// Start RPZ auto-sync background scheduler
	rpzAutoSyncCtx, rpzAutoSyncCancel := context.WithCancel(context.Background())
	go srv.runRPZAutoSync(rpzAutoSyncCtx)

	// Start auto-update background scheduler
	autoUpdateCtx, autoUpdateCancel := context.WithCancel(context.Background())
	go srv.runAutoUpdate(autoUpdateCtx)

	// Start fleet probe watchdog (peer DNS, self RPZ, serial drift)
	fleetProbeCtx, fleetProbeCancel := context.WithCancel(context.Background())
	go srv.runFleetProbe(fleetProbeCtx)

	r := chi.NewRouter()

	// Middleware
	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(chimw.Logger)
	r.Use(chimw.Recoverer)
	r.Use(chimw.Compress(5))

	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   cfg.CORSOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	// Block page (public, no auth — served to blocked domain visitors)
	r.Get("/blockpage", srv.handleBlockPage)
	r.Get("/blockpage/komdigi-logo.webp", srv.handleBlockPageLogo)

	// Public routes (no auth)
	r.Get("/api/health", srv.handleHealth)
	r.Get("/api/version", srv.handleVersion)
	r.Post("/api/auth/login", srv.handleLogin)
	r.Post("/api/auth/register", srv.handleRegister)
	r.Get("/api/auth/check", srv.handleAuthCheck)

	// Prometheus scrape endpoint — internal, no auth
	r.Get("/api/internal/metrics", srv.handleKresdMetrics)

	// Admin update routes (no timeout — SSE streams for minutes)
	r.Route("/api/admin", func(r chi.Router) {
		r.Use(srv.authMiddleware)
		r.Use(srv.adminMiddleware)

		r.Get("/update/check", srv.handleUpdateCheck)
		r.Post("/update/execute", srv.handleUpdateExecute)
		r.Get("/update/status", srv.handleUpdateStatus)
		r.Get("/update/auto", srv.handleGetAutoUpdateConfig)
		r.Put("/update/auto", srv.handleUpdateAutoUpdateConfig)

		// DNS Lookup tool
		r.Post("/dns/lookup", srv.handleDNSLookup)

		// RPZ Komdigi
		r.Get("/rpz/config", srv.handleGetRPZConfig)
		r.Put("/rpz/config", srv.handleUpdateRPZConfig)
		r.Post("/rpz/sync", srv.handleRPZSync)
		r.Get("/rpz/stats", srv.handleRPZStats)

		// Block page config
		r.Get("/blockpage/config", srv.handleGetBlockPageConfig)
		r.Put("/blockpage/config", srv.handleUpdateBlockPageConfig)

		// DNS Filtering
		r.Get("/filters", srv.handleListFilters)
		r.Post("/filters", srv.handleAddFilter)
		r.Delete("/filters/{id}", srv.handleDeleteFilter)
		r.Put("/filters/{id}/toggle", srv.handleToggleFilter)
		r.Post("/filters/import", srv.handleImportList)
		r.Get("/filters/stats", srv.handleFilterStats)
		r.Post("/filters/apply", srv.handleApplyFilters)

		r.Get("/services", srv.handleListServices)
		r.Get("/services/{name}/logs", srv.handleServiceLogs)
		r.Post("/services/restart", srv.handleRestartService)
		r.Post("/services/restart-all", srv.handleRestartAll)

		// Docker cleanup
		r.Get("/docker/cleanup", srv.handleGetCleanupInfo)
		r.Post("/docker/cleanup", srv.handleRunCleanup)

		// Server config
		r.Get("/server/config", srv.handleGetServerConfig)
		r.Put("/server/config", srv.handleUpdateServerConfig)
	})

	// Protected API routes (require JWT, with timeout)
	r.Route("/api", func(r chi.Router) {
		r.Use(srv.authMiddleware)
		r.Use(chimw.Timeout(30 * time.Second))

		// User info
		r.Get("/auth/me", srv.handleMe)
		r.Post("/auth/change-password", srv.handleChangePassword)

		// Resolver info (live config from kresd)
		r.Get("/resolver/info", srv.handleResolverInfo)

		// Metrics endpoints (proxy to Prometheus)
		r.Route("/metrics", func(r chi.Router) {
			r.Get("/qps", srv.handleMetricsQPS)
			r.Get("/latency", srv.handleMetricsLatency)
			r.Get("/cache", srv.handleMetricsCache)
			r.Get("/dnssec", srv.handleMetricsDNSSEC)
			r.Get("/system", srv.handleMetricsSystem)
			r.Get("/upstreams", srv.handleMetricsUpstreams)
			r.Get("/overview", srv.handleMetricsOverview)
		})

		// Query log endpoints (ClickHouse)
		r.Route("/queries", func(r chi.Router) {
			r.Get("/", srv.handleQuerySearch)
			r.Get("/top-domains", srv.handleTopDomains)
			r.Get("/type-distribution", srv.handleTypeDistribution)
			r.Get("/rcode-distribution", srv.handleRcodeDistribution)
			r.Get("/protocol-distribution", srv.handleProtocolDistribution)
			r.Get("/timeline", srv.handleQueryTimeline)
		})

		// Alert endpoints
		r.Route("/alerts", func(r chi.Router) {
			r.Get("/", srv.handleListAlerts)
			r.Post("/", srv.handleCreateAlert)
			r.Put("/{id}", srv.handleUpdateAlert)
			r.Delete("/{id}", srv.handleDeleteAlert)
			r.Get("/history", srv.handleAlertHistory)
		})

		// Notification config (Telegram) + fleet probe status
		r.Get("/notify/config", srv.handleGetNotifyConfig)
		r.Put("/notify/config", srv.handleUpdateNotifyConfig)
		r.Post("/notify/test", srv.handleTestNotify)
		r.Get("/fleet/probe", srv.handleFleetProbeStatus)

		// WebSocket for real-time updates
		r.Get("/ws/live", srv.handleWebSocket)
	})

	// Cluster agent API (machine-to-machine, cluster token auth)
	r.Route("/api/cluster/agent", func(r chi.Router) {
		r.Use(srv.clusterTokenMiddleware)
		r.Get("/health", srv.handleHealth)
		r.Get("/metrics/overview", srv.handleMetricsOverview)
		r.Get("/metrics/qps", srv.handleMetricsQPS)
		r.Get("/metrics/latency", srv.handleMetricsLatency)
		r.Get("/metrics/cache", srv.handleMetricsCache)
		r.Get("/metrics/system", srv.handleMetricsSystem)
		r.Get("/queries/top-domains", srv.handleTopDomains)
		r.Get("/version", srv.handleVersion)
		r.Post("/update/execute", srv.handleUpdateExecute)
		r.Get("/update/check", srv.handleUpdateCheck)
		r.Get("/update/status", srv.handleUpdateStatus)
		r.Post("/pair", srv.handleClusterPair)
		r.Get("/docker/cleanup", srv.handleGetCleanupInfo)
		r.Post("/docker/cleanup", srv.handleRunCleanup)

		// Operational levers. Every incident of 12-13 Aug 2026 needed one of
		// these and none was reachable without SSH: restart dnsdist (stale
		// backend IP), trigger/inspect RPZ sync (silent half-loaded filter),
		// read the fleet watchdog. All reuse the local admin handlers — same
		// capability, cluster-token auth. Deliberately an allowlist: no
		// arbitrary exec, a leaked token stops at restart/sync.
		r.Get("/services", srv.handleListServices)
		r.Get("/services/{name}/logs", srv.handleServiceLogs)
		r.Post("/services/restart", srv.handleRestartService)
		r.Get("/rpz/config", srv.handleGetRPZConfig)
		r.Get("/rpz/stats", srv.handleRPZStats)
		r.Post("/rpz/sync", srv.handleRPZSync)
		r.Get("/fleet/probe", srv.handleFleetProbeStatus)
		r.Get("/resolver/info", srv.handleResolverInfo)
	})

	// Cluster controller API (JWT + admin auth)
	r.Route("/api/cluster", func(r chi.Router) {
		r.Use(srv.authMiddleware)

		// Any authenticated user can read cluster config
		r.Get("/config", srv.handleClusterConfig)

		// Admin-only routes
		r.Group(func(r chi.Router) {
			r.Use(srv.adminMiddleware)
			r.Put("/config", srv.handleUpdateClusterConfig)
			r.Get("/nodes", srv.handleListNodes)
			r.Post("/nodes", srv.handleAddNode)
			r.Put("/nodes/{id}", srv.handleUpdateNode)
			r.Delete("/nodes/{id}", srv.handleDeleteNode)
			r.Get("/nodes/{id}/metrics", srv.handleNodeMetrics)
			r.Post("/nodes/{id}/update", srv.handlePushNodeUpdate)
			r.Post("/nodes/update-all", srv.handlePushUpdateAll)
			r.Get("/nodes/{id}/cleanup", srv.handleProxyNodeCleanupInfo)
			r.Post("/nodes/{id}/cleanup", srv.handleProxyNodeCleanup)
			r.Get("/nodes/{id}/services", srv.handleProxyNodeServices)
			r.Get("/nodes/{id}/services/{name}/logs", srv.handleProxyNodeServiceLogs)
			r.Post("/nodes/{id}/services/restart", srv.handleProxyNodeServiceRestart)
			r.Get("/nodes/{id}/rpz/config", srv.handleProxyNodeRPZConfig)
			r.Get("/nodes/{id}/rpz/stats", srv.handleProxyNodeRPZStats)
			r.Post("/nodes/{id}/rpz/sync", srv.handleProxyNodeRPZSync)
			r.Get("/nodes/{id}/fleet-probe", srv.handleProxyNodeFleetProbe)
			r.Get("/nodes/{id}/resolver-info", srv.handleProxyNodeResolverInfo)
			r.Get("/overview", srv.handleClusterOverview)
		})
	})

	cleanup := func() {
		fleetProbeCancel()
		autoUpdateCancel()
		rpzAutoSyncCancel()
		if srv.pollerCancel != nil {
			srv.pollerCancel()
		}
		ch.Close()
		pg.Close()
		rdb.Close()
	}

	return r, cleanup, nil
}

func initPostgres(pool *pgxpool.Pool) error {
	ctx := context.Background()
	queries := []string{
		`CREATE TABLE IF NOT EXISTS alert_rules (
			id SERIAL PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			metric VARCHAR(100) NOT NULL,
			condition VARCHAR(20) NOT NULL,
			threshold DOUBLE PRECISION NOT NULL,
			duration_sec INT NOT NULL DEFAULT 60,
			enabled BOOLEAN NOT NULL DEFAULT true,
			notify_channels JSONB NOT NULL DEFAULT '[]',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS alert_events (
			id SERIAL PRIMARY KEY,
			rule_id INT REFERENCES alert_rules(id) ON DELETE CASCADE,
			status VARCHAR(20) NOT NULL,
			value DOUBLE PRECISION,
			message TEXT,
			fired_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			resolved_at TIMESTAMPTZ
		)`,
		`CREATE TABLE IF NOT EXISTS users (
			id SERIAL PRIMARY KEY,
			username VARCHAR(100) UNIQUE NOT NULL,
			password_hash VARCHAR(255) NOT NULL,
			role VARCHAR(20) NOT NULL DEFAULT 'viewer',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS rpz_config (
			id INT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
			enabled BOOLEAN NOT NULL DEFAULT false,
			master_servers TEXT NOT NULL DEFAULT '139.255.196.202,182.23.79.202',
			zone_name VARCHAR(255) NOT NULL DEFAULT 'trustpositifkominfo',
			last_sync TIMESTAMPTZ,
			last_sync_status VARCHAR(20) DEFAULT '',
			last_sync_error TEXT DEFAULT '',
			domain_count INT NOT NULL DEFAULT 0,
			file_size_bytes BIGINT NOT NULL DEFAULT 0,
			sync_duration_ms INT NOT NULL DEFAULT 0,
			auto_sync_enabled BOOLEAN NOT NULL DEFAULT false,
			auto_sync_interval_hours INT NOT NULL DEFAULT 24,
			auto_sync_hour INT NOT NULL DEFAULT 2,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		// Migrate: add auto_sync columns if missing
		`ALTER TABLE rpz_config ADD COLUMN IF NOT EXISTS auto_sync_enabled BOOLEAN NOT NULL DEFAULT false`,
		`ALTER TABLE rpz_config ADD COLUMN IF NOT EXISTS auto_sync_interval_hours INT NOT NULL DEFAULT 24`,
		`ALTER TABLE rpz_config ADD COLUMN IF NOT EXISTS auto_sync_hour INT NOT NULL DEFAULT 2`,
		// Add zone_serial: lets auto-sync skip a 1.1GB AXFR when Komdigi's serial is unchanged
		`ALTER TABLE rpz_config ADD COLUMN IF NOT EXISTS zone_serial BIGINT NOT NULL DEFAULT 0`,
		// Seed rpz_config row + migrate old master_servers
		`INSERT INTO rpz_config (id, master_servers) VALUES (1, '139.255.196.202,182.23.79.202') ON CONFLICT DO NOTHING`,
		// Drop the decommissioned master (juknis Ver.260326: "IP 103.154.123.130 sudah tidak
		// digunakan"). It black-holes TCP/53, so it only adds AXFR timeout and, being last in
		// the list, its "no reply" became the error recorded for the whole sync.
		`UPDATE rpz_config SET master_servers = '139.255.196.202,182.23.79.202', updated_at = NOW()
		 WHERE id = 1 AND master_servers LIKE '%103.154.123.130%'`,
		`CREATE TABLE IF NOT EXISTS blockpage_config (
			id INT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
			title VARCHAR(255) NOT NULL DEFAULT 'Akses Diblokir',
			subtitle VARCHAR(500) NOT NULL DEFAULT 'Domain ini telah diblokir oleh administrator jaringan melalui DNS filtering.',
			message VARCHAR(500) NOT NULL DEFAULT 'Jika Anda merasa ini adalah kesalahan, silakan hubungi administrator.',
			contact VARCHAR(255) NOT NULL DEFAULT '',
			bg_color VARCHAR(20) NOT NULL DEFAULT '#0f172a',
			accent_color VARCHAR(20) NOT NULL DEFAULT '#ef4444',
			show_domain BOOLEAN NOT NULL DEFAULT true,
			show_logo BOOLEAN NOT NULL DEFAULT true,
			footer_text VARCHAR(255) NOT NULL DEFAULT 'DNS Filter — KResD Manager',
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS filter_rules (
			id SERIAL PRIMARY KEY,
			domain VARCHAR(255) NOT NULL,
			action VARCHAR(20) NOT NULL DEFAULT 'block',
			category VARCHAR(50) NOT NULL DEFAULT 'custom',
			enabled BOOLEAN NOT NULL DEFAULT true,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS filter_lists (
			id SERIAL PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			url TEXT NOT NULL DEFAULT '',
			category VARCHAR(50) NOT NULL DEFAULT 'ads',
			enabled BOOLEAN NOT NULL DEFAULT true,
			domain_count INT NOT NULL DEFAULT 0,
			last_updated TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS cluster_config (
			id INT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
			node_role VARCHAR(20) NOT NULL DEFAULT 'standalone',
			node_name VARCHAR(255) NOT NULL DEFAULT '',
			node_domain VARCHAR(255) NOT NULL DEFAULT '',
			controller_domain VARCHAR(255) DEFAULT '',
			controller_token VARCHAR(255) DEFAULT '',
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS cluster_nodes (
			id SERIAL PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			domain VARCHAR(255) NOT NULL UNIQUE,
			api_token VARCHAR(255) NOT NULL,
			status VARCHAR(20) NOT NULL DEFAULT 'pending',
			version VARCHAR(50) DEFAULT '',
			last_seen_at TIMESTAMPTZ,
			last_error TEXT DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS server_config (
			id INT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
			timezone VARCHAR(50) NOT NULL DEFAULT 'Asia/Jakarta',
			allowed_subnets TEXT NOT NULL DEFAULT '',
			retention_query_days INT NOT NULL DEFAULT 30,
			retention_metrics_days INT NOT NULL DEFAULT 15,
			auto_update_enabled BOOLEAN NOT NULL DEFAULT false,
			auto_update_hour INT NOT NULL DEFAULT 3,
			auto_update_day INT NOT NULL DEFAULT 0,
			last_auto_update TIMESTAMPTZ,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`INSERT INTO server_config (id) VALUES (1) ON CONFLICT DO NOTHING`,
		// Comma-separated peer IPs probed by the fleet watchdog (empty = probe nothing)
		`ALTER TABLE server_config ADD COLUMN IF NOT EXISTS probe_peers TEXT NOT NULL DEFAULT ''`,
		`CREATE TABLE IF NOT EXISTS notify_config (
			id INT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
			telegram_enabled BOOLEAN NOT NULL DEFAULT false,
			telegram_bot_token TEXT NOT NULL DEFAULT '',
			telegram_chat_id TEXT NOT NULL DEFAULT '',
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`INSERT INTO notify_config (id) VALUES (1) ON CONFLICT DO NOTHING`,
		`ALTER TABLE server_config ADD COLUMN IF NOT EXISTS allowed_subnets TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE server_config ADD COLUMN IF NOT EXISTS retention_query_days INT NOT NULL DEFAULT 30`,
		`ALTER TABLE server_config ADD COLUMN IF NOT EXISTS retention_metrics_days INT NOT NULL DEFAULT 15`,
		`ALTER TABLE server_config ADD COLUMN IF NOT EXISTS auto_update_enabled BOOLEAN NOT NULL DEFAULT false`,
		`ALTER TABLE server_config ADD COLUMN IF NOT EXISTS auto_update_hour INT NOT NULL DEFAULT 3`,
		`ALTER TABLE server_config ADD COLUMN IF NOT EXISTS auto_update_day INT NOT NULL DEFAULT 0`,
		`ALTER TABLE server_config ADD COLUMN IF NOT EXISTS last_auto_update TIMESTAMPTZ`,
		`CREATE TABLE IF NOT EXISTS cluster_metrics_cache (
			node_id INT REFERENCES cluster_nodes(id) ON DELETE CASCADE,
			metric_type VARCHAR(50) NOT NULL,
			data JSONB NOT NULL,
			fetched_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (node_id, metric_type)
		)`,
	}

	for _, q := range queries {
		if _, err := pool.Exec(ctx, q); err != nil {
			return fmt.Errorf("exec %q: %w", q[:50], err)
		}
	}

	log.Println("PostgreSQL schema initialized")
	return nil
}
