package db

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	dblib "gitlab.cept.gov.in/it-2.0-common/n-api-db"
	log "gitlab.cept.gov.in/it-2.0-common/n-api-log"
)

var database *dblib.DB

func Set(conn *dblib.DB) {
	database = conn
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
