package river

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/VictoriaMetrics/metrics"
	config "gitlab.cept.gov.in/it-2.0-common/api-config"
	bootstrapper "gitlab.cept.gov.in/it-2.0-common/n-api-bootstrapper"
	dblib "gitlab.cept.gov.in/it-2.0-common/n-api-db"
	log "gitlab.cept.gov.in/it-2.0-common/n-api-log"
	serverHandler "gitlab.cept.gov.in/it-2.0-common/n-api-server/handler"
	otelsdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.uber.org/fx"
)

var handlers []any
var database *dblib.DB

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
	database = conn

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

func Insert(ctx context.Context, table string, req any) error {
	if database == nil {
		log.Error(ctx, "river: database not initialised; run NewRiver first")
		return fmt.Errorf("river: database not initialised; run NewRiver first")
	}

	columns, values, err := insertColumns(req)
	if err != nil {
		return err
	}
	if len(columns) == 0 {
		log.Error(ctx, "river: no db-tagged fields found on %T", req)
		return fmt.Errorf("river: no db-tagged fields found on %T", req)
	}

	query := dblib.Psql.Insert(table).Columns(columns...).Values(values...)
	if _, err := dblib.Insert(ctx, database, query); err != nil {
		return err
	}
	return nil
}

func insertColumns(req any) ([]string, []any, error) {
	v := reflect.ValueOf(req)
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			log.Error(context.Background(), "river: req is a nil pointer")
			return nil, nil, fmt.Errorf("river: req is a nil pointer")
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		log.Error(context.Background(), "river: req must be a struct or pointer to struct, got %s", v.Kind())
		return nil, nil, fmt.Errorf("river: req must be a struct or pointer to struct, got %s", v.Kind())
	}

	t := v.Type()
	columns := make([]string, 0, t.NumField())
	values := make([]any, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		column := field.Tag.Get("db")
		if idx := strings.IndexByte(column, ','); idx >= 0 {
			column = column[:idx]
		}
		column = strings.TrimSpace(column)
		if column == "" || column == "-" {
			continue
		}
		columns = append(columns, column)
		values = append(values, v.Field(i).Interface())
	}
	return columns, values, nil
}
