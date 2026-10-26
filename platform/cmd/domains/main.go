// Command domains is the domain registry (ADR 0125 §3, ADR 0167): it keeps the domains, the definition of the graphs
// (node types, link types, lifecycles, algorithms), in a database of its own and serves them over Connect
// (registry.v1.DomainService). The graphs read it at their start and follow its events (goap.registry.domain.*).
//
// Every start publishes the built-in domains (GOAP_BUILTIN_DOMAINS_DIR, else GOAP_DOMAINS_DIR/builtin, else
// lib/domains/builtin), protected, then the shipped domains of GOAP_DOMAINS_DIR, the versions not stored yet. A
// protected domain is refused every write over RPC; the command line of this binary, run against the same database,
// writes it:
//
//	domains publish <file.yaml>...   publish domain versions (a protected domain included)
//	domains protect <name>...        protect domains
//	domains unprotect <name>...      lift their protection
//	domains list                     the versions stored, with their status and protection
//
// The events of a command stay in the outbox: the service delivers them (to the bus now when GOAP_NATS_URL answers).
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/gen/goap/registry/v1/registryv1connect"
	"github.com/zimwip/goap/graph/domainsvc"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/lib/graphclient"
	"github.com/zimwip/goap/lib/identity"
	"github.com/zimwip/goap/lib/outbox"
	"github.com/zimwip/goap/lib/platform"
	"github.com/zimwip/goap/lib/telemetry"
)

func main() {
	ctx := context.Background()
	log := platform.Logger("domains")
	defer telemetry.Setup(context.Background(), log, "domains")(context.Background())
	var store domainsvc.Store = domainsvc.NewMemoryStore()
	db := platform.OptionalSQL(ctx, log, "domains", domainsvc.Migrations, domainsvc.SQLiteMigrations)
	if db != nil {
		defer db.Close()
		store = domainsvc.SQLStore{DB: db.DB, Dollar: db.Dollar}
	}
	events := platform.OptionalEvents(ctx, log)
	defer events.Close()
	svc := &domainsvc.Service{Store: store}
	relay := svc.Emit(outbox.NATS(events))
	// the platform acting by itself: the seeds and the command line (ADR 0167)
	local := domainsvc.Local(authz.With(ctx, authz.System("domains", access.RoleAdmin)))

	if len(os.Args) > 1 {
		if db == nil {
			platform.Fatal(log, "command", fmt.Errorf("the command line writes the registry's database: set GOAP_DB_DSN or GOAP_SQLITE_PATH"))
		}
		if err := command(local, svc, os.Args[1], os.Args[2:]); err != nil {
			platform.Fatal(log, os.Args[1], err)
		}
		if relay != nil {
			_ = relay.Drain(ctx) // what it cannot deliver now, the service delivers at its next start
		}
		return
	}

	dir := platform.Env("GOAP_DOMAINS_DIR", "domains")
	builtins := domainsvc.BuiltinDir(dir)
	if doms, err := svc.SeedBuiltinDomains(local, builtins); err != nil {
		platform.Fatal(log, "built-in domains", err)
	} else {
		log.Info("built-in domains", "dir", builtins, "domains", doms)
	}
	if dir != "" {
		if doms, err := svc.SeedDomains(local, dir); err != nil {
			log.Error("import domains", "dir", dir, "err", err)
		} else {
			log.Info("domains imported", "dir", dir, "domains", doms)
		}
	}
	outbox.Start(ctx, relay, log)

	// who may write a domain is the organisation's rules (ABAC, ADR 0020), read from the platform graph when a request
	// asks: the registry starts before the graph, which waits for its domains
	directory := &access.Directory{Graph: graphclient.NewClient(platform.H2CClient(), platform.Env("GOAP_GRAPH_URL", "http://localhost:8081"), telemetry.ClientOptions()...)}
	if events != nil {
		if err := directory.FollowEvents(events); err != nil {
			platform.Fatal(log, "subscribe", err)
		}
	}
	authorizer, err := access.NewAuthorizer(directory)
	if err != nil {
		platform.Fatal(log, "authorizer", err)
	}
	svc.Authz = authorizer

	srv := platform.NewServer(log, platform.Env("GOAP_HTTP_ADDR", ":8080"))
	if db != nil {
		srv.Readiness(db.Ping)
	}
	srv.Readiness(events.Ready)
	srv.Mount(registryv1connect.NewDomainServiceHandler(&domainsvc.Handler{Service: svc},
		append(telemetry.HandlerOptions(), connect.WithInterceptors(identity.CommandInterceptor()))...))
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}

// command runs a command of the command line (ADR 0167).
func command(ctx context.Context, svc *domainsvc.Service, name string, args []string) error {
	switch name {
	case "publish":
		if len(args) == 0 {
			return fmt.Errorf("usage: domains publish <file.yaml>...")
		}
		for _, f := range args {
			src, err := os.ReadFile(f)
			if err != nil {
				return err
			}
			r, issues, err := svc.ImportDomain(ctx, src, true)
			if err != nil {
				return fmt.Errorf("%s: %w %v", f, err, issues)
			}
			fmt.Printf("published %s@%s\n", r.Domain.Name, r.Domain.Version)
		}
	case "protect", "unprotect":
		if len(args) == 0 {
			return fmt.Errorf("usage: domains %s <name>...", name)
		}
		for _, n := range args {
			if err := svc.Protect(ctx, n, name == "protect"); err != nil {
				return err
			}
			fmt.Printf("%sed %s\n", name, n)
		}
	case "list":
		rs, err := svc.DomainVersions(ctx, true)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tVERSION\tSTATUS\tPROTECTED")
		for _, r := range rs {
			fmt.Fprintf(w, "%s\t%s\t%s\t%v\n", r.Domain.Name, r.Domain.Version, r.Status, r.Protected)
		}
		return w.Flush()
	default:
		return fmt.Errorf("unknown command %q (publish, protect, unprotect, list): %s", name, strings.Join(os.Args, " "))
	}
	return nil
}
