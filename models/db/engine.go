// Copyright 2014 The Gogs Authors. All rights reserved.
// Copyright 2018 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package db

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"

	_ "github.com/go-sql-driver/mysql"  // Needed for the MySQL driver
	_ "github.com/lib/pq"               // Needed for the Postgresql driver
	_ "github.com/microsoft/go-mssqldb" // Needed for the MSSQL driver

	"github.com/hanzoai/orm/relational"
	"github.com/hanzoai/xorm/core"
	"github.com/hanzoai/xorm/dialects"
	"github.com/hanzoai/xorm/names"
	"github.com/hanzoai/xorm/schemas"
)

var (
	xormEngine          *relational.Engine
	registeredModels    []any
	registeredInitFuncs []func() error
)

// IterFunc is the row callback for Session.Iterate. It mirrors the underlying
// engine's callback shape without naming it, keeping the Session surface
// backend-independent.
type IterFunc = func(idx int, bean any) error

// Rows is the ORM-agnostic row cursor returned by Session.Rows.
type Rows interface {
	Next() bool
	Err() error
	Scan(beans ...any) error
	Close() error
}

// SQLSession is the ORM-agnostic query surface shared by an engine and a session.
// Fluent builders return the Session interface (not a concrete engine type) so the
// backend can be swapped without touching call sites; backed today by *relational.Session
// through the session wrapper.
type SQLSession interface {
	Count(...any) (int64, error)
	Decr(column string, arg ...any) Session
	Delete(...any) (int64, error)
	Truncate(...any) (int64, error)
	Exec(...any) (sql.Result, error)
	Find(any, ...any) error
	FindAndCount(any, ...any) (int64, error)
	Get(beans ...any) (bool, error)
	ID(any) Session
	In(string, ...any) Session
	Incr(column string, arg ...any) Session
	Insert(...any) (int64, error)
	Iterate(any, IterFunc) error
	Join(joinOperator string, tablename, condition any, args ...any) Session
	SQL(any, ...any) Session
	Where(any, ...any) Session
	Asc(colNames ...string) Session
	Desc(colNames ...string) Session
	Limit(limit int, start ...int) Session
	NoAutoTime() Session
	SumInt(bean any, columnName string) (res int64, err error)
	Select(string) Session
	SetExpr(string, any) Session
	NotIn(string, ...any) Session
	OrderBy(any, ...any) Session
	Exist(...any) (bool, error)
	Distinct(...string) Session
	Query(...any) ([]map[string][]byte, error)
	Cols(...string) Session
	Table(tableNameOrBean any) Session
	Context(ctx context.Context) Session
	QueryInterface(sqlOrArgs ...any) ([]map[string]any, error)
	IsTableExist(tableNameOrBean any) (bool, error)
}

// Engine is the ORM-agnostic engine surface returned by GetEngine.
type Engine interface {
	SQLSession
	Sync(...any) error
	Ping() error
}

// Session is the ORM-agnostic session surface. Second-level fluent builders used by
// call sites are declared here so the whole chain stays backend-independent.
type Session interface {
	Engine
	And(query any, args ...any) Session
	AllCols() Session
	GroupBy(keys string) Session
	Having(conditions string) Session
	MustCols(columns ...string) Session
	Or(query any, args ...any) Session
	NoAutoCondition(...bool) Session
	UseBool(columns ...string) Session
	Update(bean any, condiBean ...any) (int64, error)
	Rows(bean any) (Rows, error)
	Begin() error
	Close() error
	Commit() error
	IsInTx() bool
	Rollback() error
}

// xormQuerier is the concrete xorm query surface used only by the migration plane.
// Migrations manipulate the schema through xorm-specific types (Dialect, DBMetas,
// SyncResult), so this surface stays xorm-typed; abstracting it is a later cut.
type xormQuerier interface {
	Count(...any) (int64, error)
	Decr(column string, arg ...any) *relational.Session
	Delete(...any) (int64, error)
	Truncate(...any) (int64, error)
	Exec(...any) (sql.Result, error)
	Find(any, ...any) error
	FindAndCount(any, ...any) (int64, error)
	Get(beans ...any) (bool, error)
	ID(any) *relational.Session
	In(string, ...any) *relational.Session
	Incr(column string, arg ...any) *relational.Session
	Insert(...any) (int64, error)
	Iterate(any, relational.IterFunc) error
	Join(joinOperator string, tablename, condition any, args ...any) *relational.Session
	SQL(any, ...any) *relational.Session
	Where(any, ...any) *relational.Session
	Asc(colNames ...string) *relational.Session
	Desc(colNames ...string) *relational.Session
	Limit(limit int, start ...int) *relational.Session
	NoAutoTime() *relational.Session
	SumInt(bean any, columnName string) (res int64, err error)
	Select(string) *relational.Session
	SetExpr(string, any) *relational.Session
	NotIn(string, ...any) *relational.Session
	OrderBy(any, ...any) *relational.Session
	Exist(...any) (bool, error)
	Distinct(...string) *relational.Session
	Query(...any) ([]map[string][]byte, error)
	Cols(...string) *relational.Session
	Table(tableNameOrBean any) *relational.Session
	Context(ctx context.Context) *relational.Session
	QueryInterface(sqlOrArgs ...any) ([]map[string]any, error)
	IsTableExist(tableNameOrBean any) (bool, error)
}

// EngineMigration is the xorm engine surface used by the migration packages.
type EngineMigration interface {
	xormQuerier
	Sync(...any) error
	Ping() error
	Close() error
	DB() *core.DB
	DBMetas() ([]*schemas.Table, error)
	Dialect() dialects.Dialect
	DropTables(beans ...any) error
	NewSession() *relational.Session
	SetMapper(mapper names.Mapper)
	SyncWithOptions(opts relational.SyncOptions, beans ...any) (*relational.SyncResult, error)
	TableInfo(bean any) (*schemas.Table, error)
	TableName(bean any, includeSchema ...bool) string
}

// session adapts *relational.Session to the ORM-agnostic Session interface. xorm's fluent
// builders mutate the statement in place and return the same *relational.Session, so each
// override discards that return and hands back the receiver: no extra allocation,
// identical behavior. Terminal and tx methods are promoted from the embedded session.
type session struct{ *relational.Session }

func (s *session) Decr(column string, arg ...any) Session { s.Session.Decr(column, arg...); return s }
func (s *session) ID(id any) Session                      { s.Session.ID(id); return s }
func (s *session) In(column string, args ...any) Session  { s.Session.In(column, args...); return s }
func (s *session) Incr(column string, arg ...any) Session { s.Session.Incr(column, arg...); return s }
func (s *session) Join(op string, tableName, condition any, args ...any) Session {
	s.Session.Join(op, tableName, condition, args...)
	return s
}
func (s *session) SQL(query any, args ...any) Session      { s.Session.SQL(query, args...); return s }
func (s *session) Where(query any, args ...any) Session    { s.Session.Where(query, args...); return s }
func (s *session) Asc(colNames ...string) Session          { s.Session.Asc(colNames...); return s }
func (s *session) Desc(colNames ...string) Session         { s.Session.Desc(colNames...); return s }
func (s *session) Limit(limit int, start ...int) Session   { s.Session.Limit(limit, start...); return s }
func (s *session) NoAutoTime() Session                     { s.Session.NoAutoTime(); return s }
func (s *session) Select(str string) Session               { s.Session.Select(str); return s }
func (s *session) SetExpr(column string, expr any) Session { s.Session.SetExpr(column, expr); return s }
func (s *session) NotIn(column string, args ...any) Session {
	s.Session.NotIn(column, args...)
	return s
}
func (s *session) OrderBy(order any, args ...any) Session {
	s.Session.OrderBy(order, args...)
	return s
}
func (s *session) Distinct(colNames ...string) Session { s.Session.Distinct(colNames...); return s }
func (s *session) Cols(colNames ...string) Session     { s.Session.Cols(colNames...); return s }
func (s *session) Table(tableNameOrBean any) Session   { s.Session.Table(tableNameOrBean); return s }
func (s *session) Context(ctx context.Context) Session { s.Session.Context(ctx); return s }
func (s *session) And(query any, args ...any) Session  { s.Session.And(query, args...); return s }
func (s *session) Or(query any, args ...any) Session   { s.Session.Or(query, args...); return s }
func (s *session) AllCols() Session                    { s.Session.AllCols(); return s }
func (s *session) GroupBy(keys string) Session         { s.Session.GroupBy(keys); return s }
func (s *session) Having(conditions string) Session    { s.Session.Having(conditions); return s }
func (s *session) MustCols(columns ...string) Session  { s.Session.MustCols(columns...); return s }
func (s *session) NoAutoCondition(no ...bool) Session  { s.Session.NoAutoCondition(no...); return s }
func (s *session) UseBool(columns ...string) Session   { s.Session.UseBool(columns...); return s }
func (s *session) Iterate(bean any, fn IterFunc) error { return s.Session.Iterate(bean, fn) }

func (s *session) Rows(bean any) (Rows, error) {
	rows, err := s.Session.Rows(bean)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

var (
	_ Session         = (*session)(nil)
	_ Engine          = (*session)(nil)
	_ EngineMigration = (*relational.Engine)(nil)
)

// RegisterModel registers model, if initFuncs provided, it will be invoked after data model sync
func RegisterModel(bean any, initFunc ...func() error) {
	registeredModels = append(registeredModels, bean)
	if len(registeredInitFuncs) > 0 && initFunc[0] != nil {
		registeredInitFuncs = append(registeredInitFuncs, initFunc[0])
	}
}

// SyncAllTables sync the schemas of all tables, is required by unit test code
func SyncAllTables() error {
	_, err := xormEngine.StoreEngine("InnoDB").SyncWithOptions(relational.SyncOptions{
		WarnIfDatabaseColumnMissed: true,
	}, registeredModels...)
	return err
}

// NamesToBean return a list of beans or an error
func NamesToBean(names ...string) ([]any, error) {
	beans := []any{}
	if len(names) == 0 {
		beans = append(beans, registeredModels...)
		return beans, nil
	}
	// Need to map provided names to beans...
	beanMap := make(map[string]any)
	for _, bean := range registeredModels {
		beanMap[strings.ToLower(reflect.Indirect(reflect.ValueOf(bean)).Type().Name())] = bean
		beanMap[strings.ToLower(xormEngine.TableName(bean))] = bean
		beanMap[strings.ToLower(xormEngine.TableName(bean, true))] = bean
	}

	gotBean := make(map[any]bool)
	for _, name := range names {
		bean, ok := beanMap[strings.ToLower(strings.TrimSpace(name))]
		if !ok {
			return nil, fmt.Errorf("no table found that matches: %s", name)
		}
		if !gotBean[bean] {
			beans = append(beans, bean)
			gotBean[bean] = true
		}
	}
	return beans, nil
}

// MaxBatchInsertSize returns the table's max batch insert size
func MaxBatchInsertSize(bean any) int {
	t, err := xormEngine.TableInfo(bean)
	if err != nil {
		return 50
	}
	return 999 / len(t.ColumnsSeq())
}

// IsTableNotEmpty returns true if table has at least one record
func IsTableNotEmpty(beanOrTableName any) (bool, error) {
	return xormEngine.Table(beanOrTableName).Exist()
}

// DeleteAllRecords will delete all the records of this table
func DeleteAllRecords(tableName string) error {
	_, err := xormEngine.Exec("DELETE FROM " + tableName)
	return err
}

// GetMaxID will return max id of the table
func GetMaxID(beanOrTableName any) (maxID int64, err error) {
	_, err = xormEngine.Select("MAX(id)").Table(beanOrTableName).Get(&maxID)
	return maxID, err
}

func SetLogSQL(ctx context.Context, on bool) {
	if s, ok := GetEngine(ctx).(*session); ok {
		s.Session.Engine().ShowSQL(on)
	}
}
