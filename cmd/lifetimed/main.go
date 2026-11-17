// Command lifetimed 是 lifetime 领域的独立服务入口（R-15.1/R-15.3）。
//
// 它只承载发现/覆盖/队列/对账竖切；Driver harness、统计引擎、复核沙箱与
// 报告服务属于 P3/P4，后续按同样方式作为独立服务接入。
//
// 用法：
//
//	lifetimed -addr :8790 -dsn "$ARTEX_PG_DSN" [-profile profile.json]
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Autumn-27/artex/lifetime"
	"github.com/Autumn-27/artex/lifetime/service"
	"github.com/Autumn-27/artex/lifetime/store"
)

func main() {
	addr := flag.String("addr", ":8790", "HTTP listen address")
	dsn := flag.String("dsn", os.Getenv("ARTEX_PG_DSN"), "PostgreSQL DSN (default $ARTEX_PG_DSN)")
	profilePath := flag.String("profile", "", "optional TaskProfile JSON file")
	flag.Parse()

	if *dsn == "" {
		log.Fatal("lifetimed: -dsn or ARTEX_PG_DSN is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := openStoreWithRetry(ctx, *dsn, 50*time.Second)
	if err != nil {
		log.Fatalf("lifetimed: open store: %v", err)
	}
	defer st.Close()

	opts := service.Options{}
	if *profilePath != "" {
		p, err := loadProfile(*profilePath)
		if err != nil {
			log.Fatalf("lifetimed: load profile: %v", err)
		}
		opts.Profile = p
		log.Printf("lifetimed: profile %s/%s policy_hash=%s", p.Name, p.Version, p.PolicyHash)
	}

	srv, err := service.New(st, opts)
	if err != nil {
		log.Fatalf("lifetimed: init service: %v", err)
	}

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		// bootstrap 会在一次请求内索引整个目标子树并写数万行覆盖账本，
		// 60s 的 WriteTimeout 会把仍在工作的 handler 连接掐断。设为 0
		// （不限制）并用 IdleTimeout 控制空闲连接。
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shCtx); err != nil {
			log.Printf("lifetimed: shutdown: %v", err)
		}
	}()

	log.Printf("lifetimed: listening on %s", *addr)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("lifetimed: serve: %v", err)
	}
}

// openStoreWithRetry 等待容器内 Postgres 接受 TCP 连接并完成 schema 初始化。
// SEC-bench 的 install bootstrap 在 `service postgresql start` 后立即启动
// lifetimed；此时 TCP 端口可能尚未就绪，单次 Open 会挂起或失败。每次尝试
// 8s 超时，总上限 timeout，成功即返回。
func openStoreWithRetry(ctx context.Context, dsn string, timeout time.Duration) (*store.Store, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		attemptCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		st, err := store.Open(attemptCtx, dsn)
		cancel()
		if err == nil {
			return st, nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return nil, lastErr
		}
		log.Printf("lifetimed: store not ready (%v); retrying", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func loadProfile(path string) (*lifetime.TaskProfile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p lifetime.TaskProfile
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	lifetime.ApplyTaskProfileDefaults(&p)
	if err := lifetime.ValidateTaskProfile(&p); err != nil {
		return nil, err
	}
	p.PolicyHash = lifetime.ComputePolicyHash(&p)
	return &p, nil
}
