package river

import (
	"context"
	"time"

	"github.com/VictoriaMetrics/metrics"
	config "gitlab.cept.gov.in/it-2.0-common/api-config"
	bootstrapper "gitlab.cept.gov.in/it-2.0-common/n-api-bootstrapper"
	dblib "gitlab.cept.gov.in/it-2.0-common/n-api-db"
	serverHandler "gitlab.cept.gov.in/it-2.0-common/n-api-server/handler"
	serverRoute "gitlab.cept.gov.in/it-2.0-common/n-api-server/route"
	otelsdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.uber.org/fx"

	"github.com/abhishekanandwade/river/internal/db"
)

var handlers []any

func NewRiver() {
	app := bootstrapper.New().Options(
		fx.Module(
			"Handlermodule",
			fx.Provide(handlers...),
		),
		fxDatabase,
	)
	app.WithContext(context.Background()).Run()
}

var fxDatabase = fx.Module(
	"river-database",
	fx.Invoke(initDatabase),
)

type riverDBParams struct {
	fx.In
	Config     *config.Config
	Osdktrace  *otelsdktrace.TracerProvider
	MetricsSet *metrics.Set
	LC         fx.Lifecycle
}

func initDatabase(p riverDBParams) error {
	cfg := buildDBConfig(p.Config)

	factory := dblib.NewDefaultDbFactory()
	factory.SetCollectorName("river_db_collector")

	conn, err := factory.CreateConnection(&cfg, p.Osdktrace, p.MetricsSet)
	if err != nil {
		return err
	}
	db.Set(conn)

	// Register the persistence implementation with the server library so routes
	// that declare a table (via route.Route.Table) can insert without exposing
	// the database connection to application code.
	serverRoute.SetInserter(inserter{})

	p.LC.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			return conn.PingContext(ctx)
		},
		OnStop: func(ctx context.Context) error {
			conn.Close()
			return nil
		},
	})
	return nil
}

func buildDBConfig(c *config.Config) dblib.DBConfig {
	sslmode := "disable"
	if c.Exists("db.sslmode") {
		sslmode = c.GetString("db.sslmode")
	}

	var trace bool
	if c.Exists("trace.enabled") {
		trace = c.GetBool("trace.enabled")
	}

	return dblib.DBConfig{
		DBUsername:        c.GetString("db.username"),
		DBPassword:        c.GetString("db.password"),
		DBHost:            c.GetString("db.host"),
		DBPort:            c.GetString("db.port"),
		DBDatabase:        c.GetString("db.database"),
		Schema:            c.GetString("db.schema"),
		MaxConns:          c.GetInt32("db.maxconns"),
		MinConns:          c.GetInt32("db.minconns"),
		MaxConnLifetime:   time.Duration(c.GetInt("db.maxconnlifetime")),
		MaxConnIdleTime:   time.Duration(c.GetInt("db.maxconnidletime")),
		HealthCheckPeriod: time.Duration(c.GetInt("db.healthcheckperiod")),
		SSLMode:           sslmode,
		Trace:             trace,
		AppName:           c.AppName(),
	}
}

func AddHandler(constructor any) {
	handlers = append(handlers, fx.Annotate(
		constructor,
		fx.As(new(serverHandler.Handler)),
		fx.ResultTags(serverHandler.ServerControllersGroupTag),
	))
}

// inserter adapts river's internal persistence to the server library's
// route.Inserter interface. Routes that declare a table trigger this insert
// automatically; application code never calls it directly.
type inserter struct{}

func (inserter) Insert(ctx context.Context, table string, req any) error {
	return db.Insert(ctx, table, req)
}
