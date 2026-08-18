package db

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	sq "github.com/Masterminds/squirrel"
	"github.com/jackc/pgx/v5"
	dblib "gitlab.cept.gov.in/it-2.0-common/n-api-db"
	log "gitlab.cept.gov.in/it-2.0-common/n-api-log"
)

var database *dblib.DB

func Set(conn *dblib.DB) {
	database = conn
}

// Insert writes req as a new row in table. Column names are resolved from each
// exported field's db tag (falling back to json, uri, then the snake_cased
// field name); pointer values are dereferenced and nil pointers become NULL.
func Insert(ctx context.Context, table string, req any) error {
	if database == nil {
		return notReady(ctx)
	}

	cols, err := columnsOf(req)
	if err != nil {
		return err
	}

	names := make([]string, 0, len(cols))
	values := make([]any, 0, len(cols))
	for _, c := range cols {
		names = append(names, c.name)
		values = append(values, c.value)
	}
	if len(names) == 0 {
		log.Error(ctx, "river: no persistable fields found on %T", req)
		return fmt.Errorf("river: no persistable fields found on %T", req)
	}

	query := dblib.Psql.Insert(table).Columns(names...).Values(values...)
	_, err = dblib.Insert(ctx, database, query)
	return err
}

// Update sets only the fields present in req on the row(s) identified by the
// primary key, leaving absent fields untouched. A field is "present" when it is
// a non-nil pointer or a non-zero value. The primary key is the field tagged
// `river:"pk"` (or, if none is tagged, the column named "id"); it is used in the
// WHERE clause and never in SET. An update without a key is refused so it can
// never affect every row. When no row matches, pgx.ErrNoRows is returned.
func Update(ctx context.Context, table string, req any) error {
	if database == nil {
		return notReady(ctx)
	}

	cols, err := columnsOf(req)
	if err != nil {
		return err
	}

	where := sq.Eq{}
	builder := dblib.Psql.Update(table)
	setCount := 0
	for _, c := range cols {
		if c.isKey {
			if !c.present {
				return fmt.Errorf("river: update requires a non-zero primary key %q", c.name)
			}
			where[c.name] = c.value
			continue
		}
		if c.present {
			builder = builder.Set(c.name, c.value)
			setCount++
		}
	}
	if len(where) == 0 {
		return fmt.Errorf(`river: update on %q requires a primary key; tag a field with river:"pk" or name it "id"`, table)
	}
	if setCount == 0 {
		return fmt.Errorf("river: update on %q has no fields to set", table)
	}

	tag, err := dblib.Update(ctx, database, builder.Where(where))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// Delete removes the row(s) identified by the primary key carried in req. The
// primary key is the field tagged `river:"pk"` (or, if none is tagged, the
// column named "id"). A delete without a key is refused so it can never remove
// every row. When no row matches, pgx.ErrNoRows is returned.
func Delete(ctx context.Context, table string, req any) error {
	if database == nil {
		return notReady(ctx)
	}

	cols, err := columnsOf(req)
	if err != nil {
		return err
	}

	where := sq.Eq{}
	for _, c := range cols {
		if !c.isKey {
			continue
		}
		if !c.present {
			return fmt.Errorf("river: delete requires a non-zero primary key %q", c.name)
		}
		where[c.name] = c.value
	}
	if len(where) == 0 {
		return fmt.Errorf(`river: delete on %q requires a primary key; tag a field with river:"pk" or name it "id"`, table)
	}

	tag, err := dblib.Delete(ctx, database, dblib.Psql.Delete(table).Where(where))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func notReady(ctx context.Context) error {
	log.Error(ctx, "river: database not initialised; run NewRiver first")
	return fmt.Errorf("river: database not initialised; run NewRiver first")
}

// column is a single resolved field of a request struct.
type column struct {
	name    string
	value   any
	isKey   bool
	present bool
}

// columnsOf reflects over req (a struct or a pointer to one) and resolves each
// exported field into a column: its database column name, its dereferenced
// value, whether it is the primary key, and whether it is present.
func columnsOf(req any) ([]column, error) {
	v := reflect.ValueOf(req)
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil, fmt.Errorf("river: req is a nil pointer")
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil, fmt.Errorf("river: req must be a struct or pointer to struct, got %s", v.Kind())
	}

	t := v.Type()
	cols := make([]column, 0, t.NumField())
	explicitKey := false
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		name := columnName(field)
		if name == "" {
			continue
		}
		fv := v.Field(i)
		key := isPKField(field)
		if key {
			explicitKey = true
		}
		cols = append(cols, column{
			name:    name,
			value:   deref(fv),
			isKey:   key,
			present: isPresent(fv),
		})
	}

	// When no field is explicitly tagged as the primary key, treat a column
	// named "id" as the key so the common case needs no extra tags.
	if !explicitKey {
		for i := range cols {
			if strings.EqualFold(cols[i].name, "id") {
				cols[i].isKey = true
				break
			}
		}
	}
	return cols, nil
}

// columnName resolves the database column for a struct field, preferring the db
// tag, then json, then uri, and finally the snake_cased field name. It returns
// "" when the field is explicitly skipped with a "-" tag.
func columnName(f reflect.StructField) string {
	for _, key := range []string{"db", "json", "uri"} {
		tag, ok := f.Tag.Lookup(key)
		if !ok {
			continue
		}
		name := firstTagValue(tag)
		if name == "-" {
			return ""
		}
		if name != "" {
			return name
		}
	}
	return toSnakeCase(f.Name)
}

// isPKField reports whether a field is tagged as the primary key via
// `river:"pk"` (also accepting key/primarykey/primary_key).
func isPKField(f reflect.StructField) bool {
	tag, ok := f.Tag.Lookup("river")
	if !ok {
		return false
	}
	for _, part := range strings.Split(tag, ",") {
		switch strings.ToLower(strings.TrimSpace(part)) {
		case "pk", "key", "primarykey", "primary_key":
			return true
		}
	}
	return false
}

// isPresent reports whether a field value should take part in an operation: a
// pointer or interface is present when non-nil; any other value when non-zero.
func isPresent(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		return !v.IsNil()
	default:
		return !v.IsZero()
	}
}

// deref returns the underlying value of pointers/interfaces so the database
// driver receives concrete values (and nil for nil pointers).
func deref(v reflect.Value) any {
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	return v.Interface()
}

func firstTagValue(tag string) string {
	if i := strings.IndexByte(tag, ','); i >= 0 {
		tag = tag[:i]
	}
	return strings.TrimSpace(tag)
}

// toSnakeCase converts an exported Go field name to snake_case (FirstName ->
// first_name). It is only a fallback; db/json/uri tags take precedence.
func toSnakeCase(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r + ('a' - 'A'))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
