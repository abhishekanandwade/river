package river

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	bootstrapper "gitlab.cept.gov.in/it-2.0-common/n-api-bootstrapper"
	dblib "gitlab.cept.gov.in/it-2.0-common/n-api-db"
	serverHandler "gitlab.cept.gov.in/it-2.0-common/n-api-server/handler"
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
		fx.Populate(&database),
	)
	app.WithContext(context.Background()).Run()
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
		return fmt.Errorf("river: database not initialised; run NewRiver first")
	}

	columns, values, err := insertColumns(req)
	if err != nil {
		return err
	}
	if len(columns) == 0 {
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
			return nil, nil, fmt.Errorf("river: req is a nil pointer")
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
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
