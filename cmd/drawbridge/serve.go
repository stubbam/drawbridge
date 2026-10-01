package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/stuffam/drawbridge/internal/api"
	"github.com/stuffam/drawbridge/internal/auth"
	"github.com/stuffam/drawbridge/internal/control"
	"github.com/stuffam/drawbridge/internal/diag"
	"github.com/stuffam/drawbridge/internal/firewall"
	"github.com/stuffam/drawbridge/internal/keys"
	"github.com/stuffam/drawbridge/internal/lan"
	"github.com/stuffam/drawbridge/internal/model"
	"github.com/stuffam/drawbridge/internal/reconcile"
	"github.com/stuffam/drawbridge/internal/sdnotify"
	"github.com/stuffam/drawbridge/internal/service"
	"github.com/stuffam/drawbridge/internal/store"
	"github.com/stuffam/drawbridge/internal/tlscert"
	"github.com/stuffam/drawbridge/internal/version"
	"github.com/stuffam/drawbridge/internal/webui"
	"github.com/stuffam/drawbridge/internal/wg"
)

// defaultListen is every address, IPv4 and IPv6, on the admin port. The allowlist and
// the firewall keep it to the LAN and the VPN (docs/PLAN.md §6.5).
var defaultListen = ":" + strconv.Itoa(model.AdminPort)

func serve(ctx context.Context, args []string, stderr io.Writer) int {
	flags := newFlagSet("serve", stderr)
	listen := flags.String("listen", defaultListen, "`address` for the web UI and API")
	dbPath := flags.String("db", defaultDB, "database `file`")
	secret := flags.String("secret-key", defaultSecret, "at-rest encryption key `file`")
	socket := flags.String("control", defaultControl, "control socket `path` for the CLI")
	drift := flags.Duration("drift-interval", 30*time.Second, "how often to check for and correct drift")
	sessionInterval := flags.Duration("session-interval", 5*time.Second, "how often to poll clients for connect, disconnect, and roam events")
	trafficRawInterval := flags.Duration("traffic-raw-interval", service.DefaultTrafficRawInterval,
		"how wide a bucket the traffic-history sampler writes; smaller means more DB writes (docs/PLAN.md §6.4)")
	trafficRawRetention := flags.Duration("traffic-raw-retention", service.DefaultTrafficRawRetention,
		"how long raw traffic-history buckets are kept before being rolled up into hourly ones")
	trafficHourlyRetention := flags.Duration("traffic-hourly-retention", service.DefaultTrafficHourlyRetention,
		"how long hourly traffic-history buckets are kept")
	trafficRetentionInterval := flags.Duration("traffic-retention-interval", 24*time.Hour,
		"how often the traffic-history rollup-and-prune job runs")
	tlsDir := flags.String("tls-dir", "", "`directory` of the web UI's TLS certificate (default: tls/ next to the database)")
	backend := flags.String("backend", "kernel", "WireGuard `backend`: kernel, or fake to develop the web UI without root or WireGuard (nothing reaches the kernel)")
	pos, err := parseArgs(flags, args)
	if err != nil {
		return flagStatus(err)
	}
	if len(pos) > 0 {
		fmt.Fprintf(stderr, "drawbridge serve: unexpected argument %q\n", pos[0])
		return 2
	}
	if *backend != "kernel" && *backend != "fake" {
		fmt.Fprintf(stderr, "drawbridge serve: --backend must be kernel or fake, not %q\n", *backend)
		return 2
	}

	log := newLogger(stderr)
	web, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Error("can't listen", "address", *listen, "err", err)
		return 1
	}
	defer web.Close()

	lanCache := &lan.Cache{TTL: 10 * time.Second, OnError: func(err error) {
		log.Warn("can't detect the LAN's subnets", "err", err)
	}}
	svc, closeSvc, err := openService(ctx, *dbPath, *secret, *backend == "fake", lanCache.Prefixes, log)
	if err != nil {
		log.Error("can't start", "err", err)
		return 1
	}
	defer closeSvc()
	svc.TrafficRawInterval = *trafficRawInterval
	svc.TrafficRawRetention = *trafficRawRetention
	svc.TrackInterval = *sessionInterval
	svc.TrafficHourlyRetention = *trafficHourlyRetention

	if *tlsDir == "" {
		*tlsDir = filepath.Join(filepath.Dir(*dbPath), "tls")
	}
	var vpnAddrs []netip.Addr
	if st, err := svc.Settings(ctx); err == nil {
		if srv, err := st.ServerAddrs(); err == nil {
			vpnAddrs = []netip.Addr{srv.IPv4, srv.IPv6}
		}
	}
	cert, err := loadCertificate(*tlsDir, vpnAddrs, log)
	if err != nil {
		log.Error("can't start", "err", err)
		return 1
	}

	host := diag.NewHost(filepath.Dir(*dbPath))
	host.CertNotAfter = cert.Leaf.NotAfter
	svc.Diag = &host

	ctl, err := control.Listen(*socket)
	if err != nil {
		log.Error("can't start", "err", err)
		return 1
	}
	defer ctl.Close()

	d := daemon{web: web, control: ctl, svc: svc, drift: *drift, sessionInterval: *sessionInterval,
		trafficRetentionInterval: *trafficRetentionInterval, log: log,
		tls: tlscert.Config(cert), fingerprint: tlscert.Fingerprint(cert),
		allowed: allowlistFor(svc, lanCache.Prefixes)}
	if err := d.run(ctx); err != nil {
		log.Error("stopped", "err", err)
		return 1
	}
	return 0
}

// loadCertificate returns the web UI's certificate, creating it on first use (or when
// it's about to expire) for this machine's names, its LAN addresses, and its VPN
// addresses.
func loadCertificate(dir string, vpnAddrs []netip.Addr, log *slog.Logger) (tls.Certificate, error) {
	host, _ := os.Hostname()
	addrs, err := lan.HostAddrs()
	if err != nil {
		log.Warn("can't list this machine's LAN addresses for the TLS certificate", "err", err)
	}
	addrs = append(addrs, vpnAddrs...)
	cert, created, err := tlscert.Ensure(dir, tlscert.DefaultNames(host, addrs...), time.Now())
	if err != nil {
		return tls.Certificate{}, err
	}
	if created {
		log.Info("created a self-signed TLS certificate for the web UI", "dir", dir)
	}
	// Browsers warn about a self-signed certificate; this is how the admin can tell
	// that the warning is about this one.
	log.Info("web UI TLS certificate", "sha256", tlscert.Fingerprint(cert), "expires", cert.Leaf.NotAfter.Format(time.DateOnly))
	return cert, nil
}

// allowlistFor returns who may use the web UI: loopback, link-local, the VPN's subnets,
// the admin's extra sources, and the LAN's (docs/PLAN.md §6.5).
func allowlistFor(svc *service.Service, lanPrefixes func() []netip.Prefix) func(context.Context) []netip.Prefix {
	return func(ctx context.Context) []netip.Prefix {
		var sources []netip.Prefix
		if s, err := svc.Settings(ctx); err == nil {
			sources = s.AdminSources()
		}
		return lan.Allowlist(sources, lanPrefixes())
	}
}

// openService opens the database (initializing it on first use), the kernel backend,
// and the reconciler.
func openService(ctx context.Context, dbPath, secretPath string, fake bool, lanPrefixes func() []netip.Prefix, log *slog.Logger) (*service.Service, func(), error) {
	sealer, err := keys.LoadSealer(secretPath)
	if err != nil {
		return nil, nil, err
	}
	st, err := store.Open(ctx, dbPath, sealer)
	if err != nil {
		return nil, nil, err
	}
	if created, err := st.Initialize(ctx); err != nil {
		_ = st.Close()
		return nil, nil, err
	} else if created {
		log.Info("initialized a new server: new key pair and IPv6 prefix")
	}
	var (
		backend wg.Backend
		fw      reconcile.Firewall = firewall.NewApplier(rulesetPath(dbPath))
		closeWG                    = func() {}
	)
	if fake {
		log.Warn("using the fake WireGuard backend: the tunnel and the firewall exist only in memory")
		backend, fw = wg.NewFake(), &firewall.Memory{}
	} else {
		k, err := wg.NewKernel()
		if err != nil {
			_ = st.Close()
			return nil, nil, err
		}
		backend, closeWG = k, func() { _ = k.Close() }
	}
	rec := &reconcile.Reconciler{
		State:     st,
		WG:        backend,
		Firewall:  fw,
		Lock:      reconcile.FileLock{Path: lockPath(dbPath)},
		Log:       log,
		AdminPort: model.AdminPort,
		LAN:       lanPrefixes,
	}
	if fake {
		// The fake tunnel starts up, as drawbridge-tunnel.service would bring the real one.
		if _, err := rec.Up(ctx); err != nil {
			_ = st.Close()
			return nil, nil, err
		}
	}
	svc := &service.Service{
		Store:   st,
		Rec:     rec,
		WG:      backend,
		Log:     log,
		Hasher:  auth.NewHasher(auth.DefaultParams),
		Limiter: auth.NewLimiter(),
	}
	if fake {
		// No real resolver on the fake tunnel's addresses: pretend one answers on IPv4
		// only, so the UI's "this server" path, and its IPv6 gap, can be tried.
		svc.DNSProbe = func(_ context.Context, a netip.Addr) service.DNSProbe {
			if a.Is4() {
				return service.DNSProbe{Answered: true, Detail: "A resolver answered (fake backend)."}
			}
			return service.DNSProbe{Detail: "Nothing is listening for DNS on port 53 (fake backend)."}
		}
	}
	return svc, func() { closeWG(); _ = st.Close() }, nil
}

// daemon runs the web server, the control socket, and the drift loop. control and svc
// may be nil (the tests run the web server alone), and so may tls (plain HTTP, for
// tests) and allowed (every source).
type daemon struct {
	web     net.Listener
	control net.Listener
	svc     *service.Service
	drift   time.Duration
	// sessionInterval is how often TrackConnections polls (docs/PLAN.md §6.4).
	sessionInterval time.Duration
	// trafficRetentionInterval is how often the traffic-history rollup-and-prune job runs.
	trafficRetentionInterval time.Duration
	log                      *slog.Logger
	tls                      *tls.Config
	// fingerprint is the TLS certificate's, which admin setup-token shows.
	fingerprint string
	allowed     func(context.Context) []netip.Prefix
}

func (d daemon) run(ctx context.Context) error {
	webSrv := &http.Server{
		Handler:           api.New(api.Options{UI: webui.FS(), Service: d.svc, Log: d.log, Allowed: d.allowed}),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       time.Minute,
		IdleTimeout:       2 * time.Minute,
		// Browsers that haven't accepted the self-signed certificate fail every
		// handshake; those errors are debug noise, not news.
		ErrorLog: slog.NewLogLogger(d.log.Handler(), slog.LevelDebug),
	}
	servers := []*http.Server{webSrv}
	errc := make(chan error, 2)
	web := d.web
	if d.tls != nil {
		web = tls.NewListener(web, d.tls)
	}
	go func() { errc <- webSrv.Serve(web) }()

	if d.control != nil && d.svc != nil {
		ctlSrv := control.NewServer(d.svc, d.log, d.fingerprint)
		servers = append(servers, ctlSrv)
		go func() { errc <- ctlSrv.Serve(d.control) }()
	}

	var wg sync.WaitGroup
	loopCtx, stopLoop := context.WithCancel(ctx)
	defer stopLoop()
	if d.svc != nil {
		// Reconcile once before reporting ready, then keep correcting drift.
		d.svc.Sync(ctx)
		d.announceSetup(ctx)
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.driftLoop(loopCtx)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.connTrackLoop(loopCtx)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.trafficRetentionLoop(loopCtx)
		}()
	}

	d.log.Info("Drawbridge started", "version", version.String(), "address", d.web.Addr().String(), "tls", d.tls != nil)
	if err := sdnotify.Notify("READY=1"); err != nil {
		d.log.Warn("can't notify systemd", "err", err)
	}

	var runErr error
	select {
	case runErr = <-errc:
	case <-ctx.Done():
	}

	d.log.Info("Drawbridge stopping")
	_ = sdnotify.Notify("STOPPING=1")
	stopLoop()
	wg.Wait()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, s := range servers {
		if err := s.Shutdown(shutdownCtx); err != nil && runErr == nil {
			runErr = err
		}
	}
	if errors.Is(runErr, http.ErrServerClosed) {
		runErr = nil
	}
	return runErr
}

func (d daemon) driftLoop(ctx context.Context) {
	if d.drift <= 0 {
		return
	}
	t := time.NewTicker(d.drift)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.svc.Sync(ctx)
			d.svc.PruneSessions(ctx)
		}
	}
}

// connTrackLoop polls for client connect, disconnect, and roam events
// (Service.TrackConnections), separately from driftLoop because it runs much more often.
func (d daemon) connTrackLoop(ctx context.Context) {
	if d.sessionInterval <= 0 {
		return
	}
	t := time.NewTicker(d.sessionInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.svc.TrackConnections(ctx)
		}
	}
}

// trafficRetentionLoop rolls up and prunes traffic history (Service.TrafficRetention),
// far less often than connTrackLoop or driftLoop since it's not time-sensitive: an
// interval this long is fine even after a restart, because the job is idempotent.
func (d daemon) trafficRetentionLoop(ctx context.Context) {
	if d.trafficRetentionInterval <= 0 {
		return
	}
	t := time.NewTicker(d.trafficRetentionInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.svc.TrafficRetention(ctx)
		}
	}
}

// announceSetup logs the setup token until the admin account exists, so it's in the
// journal as well as in the package's install output (docs/PLAN.md §6.5).
func (d daemon) announceSetup(ctx context.Context) {
	needed, err := d.svc.SetupNeeded(ctx)
	if err != nil || !needed {
		return
	}
	token, err := d.svc.SetupToken(ctx)
	if err != nil {
		d.log.Warn("can't create the setup token", "err", err)
		return
	}
	d.log.Info("the admin account doesn't exist yet: open the web UI from the home network or the VPN, and enter the setup token",
		"token", token)
}
