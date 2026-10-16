module github.com/zimwip/goap/gateway

go 1.26.0

require (
	connectrpc.com/connect v1.21.0
	github.com/casbin/casbin/v2 v2.135.0 // indirect
	github.com/exaring/otelpgx v0.12.0 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgx/v5 v5.11.0
	github.com/labstack/echo-opentelemetry v0.0.3 // indirect
	github.com/labstack/echo/v5 v5.3.1
	github.com/nats-io/nats.go v1.54.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.71.0
	go.opentelemetry.io/otel v1.46.0 // indirect
	go.opentelemetry.io/otel/metric v1.46.0 // indirect
	go.opentelemetry.io/otel/trace v1.46.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	modernc.org/sqlite v1.59.0 // indirect
)

require (
	github.com/bmatcuk/doublestar/v4 v4.6.1 // indirect
	github.com/casbin/govaluate v1.3.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/felixge/httpsnoop v1.1.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/klauspost/compress v1.20.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/nats-io/nkeys v0.4.16 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	modernc.org/libc v1.75.7 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)

require (
	github.com/zimwip/goap/contracts v0.0.0
	github.com/zimwip/goap/lib v0.0.0
	golang.org/x/crypto v0.57.0
)

replace (
	github.com/zimwip/goap/contracts => ../contracts
	github.com/zimwip/goap/lib => ../lib
)
