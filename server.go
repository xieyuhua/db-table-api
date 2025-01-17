package main

import (
	"log"
	"net/http"
	"github.com/go-ozzo/ozzo-routing/access"
	"github.com/go-ozzo/ozzo-routing/slash"
	"github.com/go-ozzo/ozzo-routing/content"
	"github.com/go-ozzo/ozzo-routing/fault"
	"github.com/go-ozzo/ozzo-routing"
	"github.com/go-ozzo/ozzo-dbx"
	_ "github.com/go-sql-driver/mysql"
	"strings"
	"strconv"
	"io/ioutil"
	"encoding/json"
	"fmt"
	"db-table-api/response"
	"crypto/sha1"
	"github.com/go-ozzo/ozzo-routing/cors"
	"errors"
	
	"github.com/go-ozzo/ozzo-routing/file"
)

var (
	cfg *Config
	db  *dbx.DB
)

// Current table information
type Table struct {
	PrimaryKey    string
	Columns       []string
	booleanFields []string // 需要处理为布尔值的字段
	ignoreFields  []string // 忽略的字段
}

type Config struct {
	Debug                 bool
	ListenPort            string
	Driver                string
	DSN                   string
	database              string
	tables                []string
	table                 Table // Table info
	TablePrefix           string
	DefaultPrimaryKeyName string // 默认主键名称
	FieldNameFormat       string
	BooleanFields         map[string][]string // 需要处理为布尔值的字段
	IgnoreFields          map[string][]string // 忽略的字段
	toCamel               bool
}

// 转换字符串为驼峰格式
func toCamel(s, sep string) string {
	if len(sep) == 0 {
		return s
	}

	var b strings.Builder
	if strings.Index(s, sep) == -1 {
		fmt.Fprint(&b, strings.ToUpper(s[:1]), s[1:])
	} else {
		for _, v := range strings.Split(s, sep) {
			fmt.Fprint(&b, strings.ToUpper(v[:1]), v[1:])
		}
	}

	return b.String()
}

// string to boolean convert
func toBoolean(s string) bool {
	if len(s) == 0 {
		return false
	} else {
		if i, _ := strconv.ParseInt(s, 10, 0); i > 0 {
			return true
		} else {
			return false
		}
	}
}

func parseTable(table string) string {
	table = strings.Trim(table, " ")

	if len(cfg.TablePrefix) > 0 && !strings.HasPrefix(table, cfg.TablePrefix) {
		table = cfg.TablePrefix + table
	}

	if len(cfg.tables) != 0 {
		inTables := false
		for _, v := range cfg.tables {
			if v == table {
				inTables = true
				break
			}
		}

		if !inTables {
			errors.New(fmt.Sprintf("Table `%s` not exists in database.", table))
		}
	}

	// 获取主键以及表的所有列名称
	tk := struct {
		Column_name string
	}{
		Column_name: cfg.DefaultPrimaryKeyName,
	}
	columns := make([]string, 0)

	switch strings.ToLower(cfg.Driver) {
	case "mysql":
		db.NewQuery(fmt.Sprintf("SELECT `COLUMN_NAME` FROM `INFORMATION_SCHEMA`.`COLUMNS` WHERE `TABLE_SCHEMA` = '%v' AND `TABLE_NAME` = '%v'", cfg.database, table)).Column(&columns)
		db.NewQuery(fmt.Sprintf("SHOW KEYS FROM %v WHERE Key_name = 'PRIMARY'", table)).Row(&tk)
	}

	cfg.table.PrimaryKey = tk.Column_name
	if len(columns) > 0 {
		cfg.table.Columns = columns
	}

	booleanFields := make([]string, 0)
	ignoreFields := make([]string, 0)
	for _, k := range []string{"_", table} {
		if v, ok := cfg.BooleanFields[k]; ok {
			booleanFields = append(booleanFields, v...)
		}

		if v, ok := cfg.IgnoreFields[k]; ok && len(v) > 0 {
			ignoreFields = append(ignoreFields, v...)
		}
	}
	cfg.table.booleanFields = booleanFields
	cfg.table.ignoreFields = ignoreFields

	return table
}

type InvalidConfig struct {
	file   string
	config string
}

func (e *InvalidConfig) Error() string {
	return fmt.Sprintf("%v", e.file)
}

// 载入配置文件
func loadConfig() (*Config, error) {
	cfg := &Config{
		Debug:                 true,
		Driver:                "mysql",
		FieldNameFormat:       "original",
		toCamel:               false,
		DefaultPrimaryKeyName: "id",
	}
	filePath := "config/conf.json"
	jsonFile, err := ioutil.ReadFile(filePath)
	if err != nil {
		return nil, &InvalidConfig{file: filePath}
	}

	err = json.Unmarshal(jsonFile, &cfg)
	if err != nil {
		return nil, &InvalidConfig{file: filePath, config: string(jsonFile)}
	}

	return cfg, nil
}

func init() {
	if c, err := loadConfig(); err != nil {
		ae := err.(*InvalidConfig)
		panic("Config file read error:\nfile = " + ae.file + "\nconfig = " + ae.config)
	} else {
		cfg = c
	}

	if strings.ToLower(strings.Trim(cfg.FieldNameFormat, " ")) == "camel" {
		cfg.toCamel = true
	}

	t := strings.Split(cfg.DSN, "/")
	if len(t) < 2 {
		panic("DSN error.")
	} else {
		db, _ = dbx.Open(cfg.Driver, cfg.DSN)
		cfg.database = t[1]
		if len(cfg.tables) == 0 {
			sql := ""
			switch strings.ToLower(cfg.Driver) {
			case "mysql":
				sql = fmt.Sprintf("SELECT TABLE_NAME FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = '%s'", cfg.database)
			case "sqlite3":
				sql = "SELECT sql FROM sqlite_master WHERE sql IS NOT NULL ORDER BY rootpage ASC"
			}
			if len(sql) > 0 {
				db.NewQuery(sql).Column(&cfg.tables)
			}
		}
	}
}

func main() {
	router := routing.New()
	router.Use(
		access.Logger(log.Printf),
		slash.Remover(http.StatusMovedPermanently),
		fault.Recovery(log.Printf),
	)
	
	api := router.Group("/api")
	api.Use(
		content.TypeNegotiator(content.JSON),
		cors.Handler(cors.Options{
			AllowOrigins: "*",
			AllowHeaders: "*",
			AllowMethods: "*",
		}),
	)

	// GET /api/ping
	api.Get("/ping", func(c *routing.Context) error {
		return c.Write("OK")
	})
	
	//GET /api/table  list  tables
	api.Get("/tables", func(c *routing.Context) error {
		data := make(map[string]interface{})
		for name, v := range cfg.tables {
			data[fmt.Sprintf("%d", name)] = v
		}
		resp := &response.SuccessOneResponse{
			Success: true,
			Data:    data,
		}
		return c.Write(resp)
	})

	// 添加记录
	// POST /api/TABLE_NAME
	api.Post(`/<table:\w+>`, func(c *routing.Context) error {
		table := parseTable(c.Param("table"))
		c.Request.ParseForm()
		columns := make(dbx.Params)
		for k, v := range c.Request.PostForm {
			if k == "id" {
				continue
			}
			if len(cfg.table.Columns) > 0 {
				found := false
				for _, name := range cfg.table.Columns {
					if k == name {
						found = true
					}
				}
				if !found {
					continue
				}
			}

			columns[k] = v[0]
		}
		q := db.Insert(table, columns)
		if cfg.Debug {
			log.Println(q.SQL(), columns, fmt.Sprintf("%+v", q.Params()))
		}
		result, err := q.Execute()
		if err == nil {
			lastInsertId, _ := result.LastInsertId()
			data := make(map[string]interface{})
			data = columns
			data["id"] = lastInsertId
			resp := &response.SuccessOneResponse{
				Success: true,
				Data:    data,
			}
			return c.Write(resp)
		} else {
			error := &response.Error{
				Message: fmt.Errorf("%v", err).Error(),
			}
			resp := &response.FailResponse{
				Success: false,
				Error:   *error,
			}
			return c.Write(resp)
		}
	})

	// 获取数据列表
	// GET /api/TABLE_NAME?page=1&pageSize=100&username=a&fields=username
	api.Get(`/<table:\w+>`, func(c *routing.Context) error {
		page, _ := strconv.ParseInt(c.Query("page", "1"), 10, 64)
		pageSize, _ := strconv.ParseInt(c.Query("pageSize", "10"), 10, 64)
		table := parseTable(c.Param("table"))

		// Build database query conditions
		exp := dbx.HashExp{}
		for k, item := range c.Request.URL.Query() {
			if k == "page" || k == "pageSize" {
				continue
			}

			if len(cfg.table.Columns) > 0 {
				found := false
				for _, name := range cfg.table.Columns {
					if k == name {
						found = true
						break
					}
				}
				if !found {
					log.Println(fmt.Sprintf("Param `%v` is not a valid field name for `%v` table", k, table))
					continue
				}
			}

			if len(item) == 1 {
				// username=a
				exp[k] = item[0]
			} else {
				// username=a&username=b
				vv := make([]interface{}, len(item))
				for i, v := range item {
					vv[i] = v
				}
				exp[k] = vv
			}
		}
		if cfg.Debug {
			log.Println(exp)
		}

		// Select columns process
		cols := make([]string, 0)
		fields := c.Query("fields")
		if len(fields) > 0 {
			if len(cfg.table.Columns) > 0 {
				for _, v := range strings.Split(fields, ",") {
					for _, name := range cfg.table.Columns {
						if v == name {
							cols = append(cols, v)
						}
					}
				}
			} else {
				cols = strings.Split(fields, ",")
			}
		} else {
			cols = append(cols, "*")
		}

		row := dbx.NullStringMap{}
		sq := db.Select().From(table).Where(exp)
		var totalCount int64
		sq.Select("COUNT(*)").Row(&totalCount)
		totalPages := (totalCount + pageSize - 1) / pageSize
		sq.Select(cols...).Offset((page - 1) * pageSize).Limit(pageSize)
		if cfg.Debug {
			log.Println(sq.Build().SQL(), fmt.Sprintf("%+v", sq.Build().Params()))
		}
		rows, err := sq.Rows()
		if err == nil {
			d := &response.SuccessListData{}
			d.Items = make([]interface{}, 0)

			for rows.Next() {
				rows.ScanMap(row)
				t := make(map[string]interface{})
				for name, v := range row {
					ignore := false
					for _, v := range cfg.table.ignoreFields {
						if name == v {
							ignore = true
							break
						}
					}
					if ignore {
						continue
					}

					// Process boolean value
					toBool := false
					for _, v := range cfg.table.booleanFields {
						if name == v {
							toBool = true
							break
						}
					}

					if cfg.toCamel {
						name = toCamel(name, "_")
					}

					if toBool {
						t[name] = toBoolean(v.String)
					} else {
						t[name] = v.String
					}
				}
				d.Items = append(d.Items, t)
			}

			d.Meta = map[string]int64{
				"totalCount":  totalCount,
				"pageCount":   totalPages,
				"currentPage": page,
				"perPage":     pageSize,
			}
			resp := &response.SuccessListResponse{
				Success: true,
				Data:    *d,
			}

			etag := fmt.Sprintf(`"%s%d%d%d"`, table, totalCount, page, pageSize)
			etag = fmt.Sprintf("%x", sha1.Sum([]byte(etag)))
			c.Response.Header().Set("Etag", etag)
			if match := c.Request.Header.Get("If-None-Match"); match != "" && match == etag {
				c.Response.WriteHeader(http.StatusNotModified)
				return nil
			} else {
				return c.Write(resp)
			}

		} else {
			error := &response.Error{
				Message: fmt.Errorf("%v", err).Error(),
			}
			resp := &response.FailResponse{
				Success: false,
				Error:   *error,
			}
			return c.Write(resp)
		}
	})

	// 获取指定的数据
	// GET /api/TABLE_NAME/ID
	api.Get(`/<table:\w+>/<id:\d+>`, func(c *routing.Context) error {
		table := parseTable(c.Param("table"))
		
        //http://192.167.1.124:8795/api/session?hostname=biadu.com&os=iOS
        
		// Select columns process
		cols := make([]string, 0)
		fields := c.Query("fields")
		if len(fields) > 0 {
			if len(cfg.table.Columns) > 0 {
				for _, v := range strings.Split(fields, ",") {
					for _, name := range cfg.table.Columns {
						if v == name {
							cols = append(cols, v)
						}
					}
				}
			} else {
				cols = strings.Split(fields, ",")
			}
		} else {
			cols = append(cols, "*")
		}
		sq := db.Select(cols...).From(table).Where(dbx.HashExp{cfg.table.PrimaryKey: c.Param("id")})
		if cfg.Debug {
			log.Println(sq.Build().SQL(), fmt.Sprintf("%+v", sq.Build().Params()))
		}
		row := dbx.NullStringMap{}
		err := sq.One(row)
		if err == nil {
			data := make(map[string]interface{})
			for name, v := range row {
				ignore := false
				for _, v := range cfg.table.ignoreFields {
					if name == v {
						ignore = true
						break
					}
				}
				if ignore {
					continue
				}

				// Process boolean value
				toBool := false
				for _, v := range cfg.table.booleanFields {
					if name == v {
						toBool = true
						break
					}
				}

				if cfg.toCamel {
					name = toCamel(name, "_")
				}

				if toBool {
					data[name] = toBoolean(v.String)
				} else {
					data[name] = v.String
				}
			}
			resp := &response.SuccessOneResponse{
				Success: true,
				Data:    data,
			}
			return c.Write(resp)
		} else {
			error := &response.Error{
				Message: fmt.Errorf("%v", err).Error(),
			}
			resp := &response.FailResponse{
				Success: false,
				Error:   *error,
			}
			return c.Write(resp)
		}
	})

	// 修改表内容
	// PUT /api/TABLE_NAME/ID
	api.Put(`/<table:\w+>/<id:\d+>`, func(c *routing.Context) error {
		table := parseTable(c.Param("table"))
		id := c.Param("id")
		sq := db.Select().From(table).Where(dbx.HashExp{cfg.table.PrimaryKey: id})
		if cfg.Debug {
			log.Println(sq.Build().SQL(), fmt.Sprintf("%+v", sq.Build().Params()))
		}
		row := dbx.NullStringMap{}
		err := sq.One(row)
		if err == nil {
			c.Request.ParseForm()
			columns := make(dbx.Params)
			for k, v := range c.Request.PostForm {
				if k == cfg.table.PrimaryKey {
					continue
				}
				if len(cfg.table.Columns) > 0 {
					found := false
					for _, name := range cfg.table.Columns {
						if k == name {
							found = true
						}
					}
					if !found {
						continue
					}
				}

				columns[k] = v[0]
			}
			q := db.Update(table, columns, dbx.HashExp{cfg.table.PrimaryKey: id})
			if cfg.Debug {
				log.Println(sq.Build().SQL(), columns, fmt.Sprintf("%+v", sq.Build().Params()))
			}
			_, ok := q.Execute()
			if ok == nil {
				data := make(map[string]interface{})
				resp := &response.SuccessOneResponse{
					Success: true,
					Data:    data,
				}
				return c.Write(resp)
			} else {
				error := &response.Error{
					Message: fmt.Errorf("%v", err).Error(),
				}
				resp := &response.FailResponse{
					Success: false,
					Error:   *error,
				}
				return c.Write(resp)
			}
		} else {
			error := &response.Error{
				Message: fmt.Errorf("%v", err).Error(),
			}
			resp := &response.FailResponse{
				Success: false,
				Error:   *error,
			}
			return c.Write(resp)
		}
	})

	// 根据主键删除表中的指定数据
	// DELETE /TABLE_NAME/ID
	api.Delete(`/<table:\w+>/<id:\d+>`, func(c *routing.Context) error {
		table := parseTable(c.Param("table"))
		id := c.Param("id")
		q := db.Delete(table, dbx.HashExp{cfg.table.PrimaryKey: id})
		if cfg.Debug {
			log.Println(q.SQL(), fmt.Sprintf("%+v", q.Params()))
		}
		result, err := q.Execute()
		if err == nil {
			rowsAffected, _ := result.RowsAffected()
			if rowsAffected > 0 {
				data := make(map[string]interface{})
				resp := &response.SuccessOneResponse{
					Success: true,
					Data:    data,
				}
				return c.Write(resp)
			} else {
				error := &response.Error{
					Message: fmt.Sprintf("Not found id eq %v data.", id),
				}
				resp := &response.FailResponse{
					Success: false,
					Error:   *error,
				}
				return c.Write(resp)
			}
		} else {
			error := &response.Error{
				Message: fmt.Errorf("%v", err).Error(),
			}
			resp := &response.FailResponse{
				Success: false,
				Error:   *error,
			}
			return c.Write(resp)
		}

	})

    router.Get("/", file.Content("web/index.html"))
// serve files under the "ui" subdirectory
	router.Get("/*", file.Server(file.PathMap{
		"/": "/web/",
	}))
	
	http.Handle("/", router)
	addr := cfg.ListenPort
	if len(addr) == 0 {
		addr = "8080"
	}
	fmt.Println("Listen :", addr)
	err := http.ListenAndServe(":"+addr, nil)
	if err != nil {
		log.Println(err)
	}
}
