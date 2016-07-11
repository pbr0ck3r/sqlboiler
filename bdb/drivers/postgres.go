package drivers

import (
	"database/sql"
	"fmt"

	// Side-effect import sql driver
	_ "github.com/lib/pq"
	"github.com/nullbio/sqlboiler/bdb"
)

// PostgresDriver holds the database connection string and a handle
// to the database connection.
type PostgresDriver struct {
	connStr string
	dbConn  *sql.DB
}

// NewPostgresDriver takes the database connection details as parameters and
// returns a pointer to a PostgresDriver object. Note that it is required to
// call PostgresDriver.Open() and PostgresDriver.Close() to open and close
// the database connection once an object has been obtained.
func NewPostgresDriver(user, pass, dbname, host string, port int, sslmode string) *PostgresDriver {
	driver := PostgresDriver{
		connStr: fmt.Sprintf("user=%s password=%s dbname=%s host=%s port=%d sslmode=%s",
			user, pass, dbname, host, port, sslmode),
	}

	return &driver
}

// Open opens the database connection using the connection string
func (p *PostgresDriver) Open() error {
	var err error
	p.dbConn, err = sql.Open("postgres", p.connStr)
	if err != nil {
		return err
	}

	return nil
}

// Close closes the database connection
func (p *PostgresDriver) Close() {
	p.dbConn.Close()
}

// TableNames connects to the postgres database and
// retrieves all table names from the information_schema where the
// table schema is public. It excludes common migration tool tables
// such as gorp_migrations
func (p *PostgresDriver) TableNames() ([]string, error) {
	var names []string

	rows, err := p.dbConn.Query(`
		select table_name from information_schema.tables
		where table_schema = 'public' and table_name not like '%migrations%'
	`)

	if err != nil {
		return nil, err
	}

	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}

	return names, nil
}

// Columns takes a table name and attempts to retrieve the table information
// from the database information_schema.columns. It retrieves the column names
// and column types and returns those as a []Column after TranslateColumnType()
// converts the SQL types to Go types, for example: "varchar" to "string"
func (p *PostgresDriver) Columns(tableName string) ([]bdb.Column, error) {
	var columns []bdb.Column

	rows, err := p.dbConn.Query(`
	select column_name, data_type, column_default, is_nullable
	from information_schema.columns
	where table_name=$1 and table_schema = 'public'
	`, tableName)

	if err != nil {
		return nil, err
	}

	defer rows.Close()
	for rows.Next() {
		var colName, colType, colDefault, Nullable string
		var defaultPtr *string
		if err := rows.Scan(&colName, &colType, &defaultPtr, &Nullable); err != nil {
			return nil, fmt.Errorf("unable to scan for table %s: %s", tableName, err)
		}

		if defaultPtr == nil {
			colDefault = ""
		} else {
			colDefault = *defaultPtr
		}

		column := bdb.Column{
			Name:       colName,
			Type:       colType,
			Default:    colDefault,
			Nullable: Nullable == "YES",
		}
		columns = append(columns, column)
	}

	return columns, nil
}

// PrimaryKeyInfo looks up the primary key for a table.
func (p *PostgresDriver) PrimaryKeyInfo(tableName string) (*bdb.PrimaryKey, error) {
	pkey := &bdb.PrimaryKey{}
	var err error

	query := `
	select tc.constraint_name
	from information_schema.table_constraints as tc
	where tc.table_name = $1 and tc.constraint_type = 'PRIMARY KEY' and tc.table_schema = 'public';`

	row := p.dbConn.QueryRow(query, tableName)
	if err = row.Scan(&pkey.Name); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	queryColumns := `
	select kcu.column_name
	from   information_schema.key_column_usage as kcu
	where  constraint_name = $1 and table_schema = 'public';`

	var rows *sql.Rows
	if rows, err = p.dbConn.Query(queryColumns, pkey.Name); err != nil {
		return nil, err
	}

	var columns []string
	for rows.Next() {
		var column string

		err = rows.Scan(&column)
		if err != nil {
			return nil, err
		}

		columns = append(columns, column)
	}

	if err = rows.Err(); err != nil {
		return nil, err
	}

	pkey.Columns = columns

	return pkey, nil
}

// ForeignKeyInfo retrieves the foreign keys for a given table name.
func (p *PostgresDriver) ForeignKeyInfo(tableName string) ([]bdb.ForeignKey, error) {
	var fkeys []bdb.ForeignKey

	query := `
	select
		tc.constraint_name,
		kcu.table_name as source_table,
		kcu.column_name as source_column,
		ccu.table_name as dest_table,
		ccu.column_name as dest_column
	from information_schema.table_constraints as tc
		inner join information_schema.key_column_usage as kcu ON tc.constraint_name = kcu.constraint_name
		inner join information_schema.constraint_column_usage as ccu ON tc.constraint_name = ccu.constraint_name
	where tc.table_name = $1 and tc.constraint_type = 'FOREIGN KEY' and tc.table_schema = 'public';`

	var rows *sql.Rows
	var err error
	if rows, err = p.dbConn.Query(query, tableName); err != nil {
		return nil, err
	}

	for rows.Next() {
		var fkey bdb.ForeignKey
		var sourceTable string

		err = rows.Scan(&fkey.Name, &sourceTable, &fkey.Column, &fkey.ForeignTable, &fkey.ForeignColumn)
		if err != nil {
			return nil, err
		}

		fkeys = append(fkeys, fkey)
	}

	if err = rows.Err(); err != nil {
		return nil, err
	}

	return fkeys, nil
}

// TranslateColumnType converts postgres database types to Go types, for example
// "varchar" to "string" and "bigint" to "int64". It returns this parsed data
// as a Column object.
func (p *PostgresDriver) TranslateColumnType(c bdb.Column) bdb.Column {
	if c.Nullable {
		switch c.Type {
		case "bigint", "bigserial":
			c.Type = "null.Int64"
		case "integer", "serial":
			c.Type = "null.Int32"
		case "smallint", "smallserial":
			c.Type = "null.Int16"
		case "decimal", "numeric", "double precision", "money":
			c.Type = "null.Float64"
		case "real":
			c.Type = "null.Float32"
		case "bit", "bit varying", "character", "character varying", "cidr", "inet", "json", "macaddr", "text", "uuid", "xml":
			c.Type = "null.String"
		case "bytea":
			c.Type = "[]byte"
		case "boolean":
			c.Type = "null.Bool"
		case "date", "interval", "time", "timestamp without time zone", "timestamp with time zone":
			c.Type = "null.Time"
		default:
			c.Type = "null.String"
		}
	} else {
		switch c.Type {
		case "bigint", "bigserial":
			c.Type = "int64"
		case "integer", "serial":
			c.Type = "int32"
		case "smallint", "smallserial":
			c.Type = "int16"
		case "decimal", "numeric", "double precision", "money":
			c.Type = "float64"
		case "real":
			c.Type = "float32"
		case "bit", "bit varying", "character", "character varying", "cidr", "inet", "json", "macaddr", "text", "uuid", "xml":
			c.Type = "string"
		case "bytea":
			c.Type = "[]byte"
		case "boolean":
			c.Type = "bool"
		case "date", "interval", "time", "timestamp without time zone", "timestamp with time zone":
			c.Type = "time.Time"
		default:
			c.Type = "string"
		}
	}

	return c
}
