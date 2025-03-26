package main

import (
	"database/sql"
	"embed"
	"encoding/csv"
	"fmt"
	"gopkg.in/yaml.v3"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	_ "github.com/jinzhu/gorm/dialects/postgres"
)

type ConfigInit struct {
	Path string `yaml:"path"`
	Port string `yaml:"port"`
	A    bool   `yaml:"a,omitempty"`
}

type Config struct {
	Spring struct {
		Datasource struct {
			URL      string `yaml:"url"`
			Username string `yaml:"username"`
			Password string `yaml:"password"`
		} `yaml:"datasource"`
	} `yaml:"spring"`
}

var db *sql.DB
var configInit ConfigInit
var notSelect = false

//go:embed static/*
var staticFiles embed.FS

func main() {
	// 读取配置文件
	configInit = loadConfigInit()
	config := loadConfig(configInit.Path)

	// 转换JDBC URL为Go格式
	goDSN := convertJdbcUrl(
		config.Spring.Datasource.URL,
		config.Spring.Datasource.Username,
		config.Spring.Datasource.Password)

	// 连接数据库
	var err error
	db, err = sql.Open("postgres", goDSN)
	if err != nil {
		log.Fatal(err)
	}
	defer func(db *sql.DB) {
		err := db.Close()
		if err != nil {
			log.Fatal(err)
		}
	}(db)

	// 初始化Gin
	r := gin.Default()
	// Serve embedded static files
	r.GET("/static/*filepath", func(c *gin.Context) {
		filePath := c.Param("filepath")
		data, err := staticFiles.ReadFile("static" + filePath)
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		c.Data(http.StatusOK, http.DetectContentType(data), data)
	})

	// Serve the embedded index.html
	r.GET("/", func(c *gin.Context) {
		data, err := staticFiles.ReadFile("static/index.html")
		if err != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", data)
	})

	r.POST("/query", ExecuteSQL)
	r.GET("/download", handleDownload)

	if strings.TrimSpace(configInit.Port) == "" {
		configInit.Port = ":8080"
	} else {
		configInit.Port = ":" + configInit.Port
	}

	err = r.Run(configInit.Port)
	if err != nil {
		log.Fatal(err)
		return
	}
}

func loadConfig(path string) *Config {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		log.Fatal(err)
	}

	return &config
}
func loadConfigInit() ConfigInit {
	data, err := os.ReadFile("config.yaml")
	if err != nil {
		log.Fatal(err)
	}

	var configInit ConfigInit
	if err := yaml.Unmarshal(data, &configInit); err != nil {
		log.Fatal(err)
	}

	notSelect = configInit.A

	return configInit
}

func convertJdbcUrl(jdbcUrl, user, pwd string) string {
	re := regexp.MustCompile(`jdbc:([^:]+)://([^:]+):(\d+)/([^?]+)`)
	matches := re.FindStringSubmatch(jdbcUrl)
	if len(matches) != 5 {
		log.Fatal("Invalid JDBC URL format")
	}
	return fmt.Sprintf("user=%s password=%s host=%s port=%s dbname=%s sslmode=disable",
		user,
		pwd,
		matches[2],
		matches[3],
		matches[4])
}

func validateQuery(sql string, notSelect bool) bool {
	if notSelect {
		return true
	} else {
		matched, _ := regexp.MatchString(`^SELECT\s+.*`, strings.ToUpper(sql))
		return matched && !strings.Contains(sql, ";")
	}
}

// ExecuteSQL handles both queries and non-query statements
func ExecuteSQL(c *gin.Context) {
	var req struct {
		SQL string `json:"sql" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Check if it's a SELECT query
	isQuery := strings.HasPrefix(strings.TrimSpace(strings.ToUpper(req.SQL)), "SELECT")

	if isQuery {
		handleQuery(c, db, req.SQL)
	} else {
		if notSelect {
			handleNonQuery(c, db, req.SQL)
		}
	}
}

func handleQuery(c *gin.Context, db *sql.DB, sqlStr string) {
	rows, err := db.Query(sqlStr)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer func(rows *sql.Rows) {
		err := rows.Close()
		if err != nil {
			log.Fatal(err)
		}
	}(rows)

	columns, _ := rows.Columns()
	result := make([]map[string]interface{}, 0)

	count := 0
	for rows.Next() && count < 5 {
		values := make([]interface{}, len(columns))
		scanArgs := make([]interface{}, len(columns))
		for i := range values {
			scanArgs[i] = &values[i]
		}

		if err := rows.Scan(scanArgs...); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		rowData := make(map[string]interface{})
		for i, col := range columns {
			switch v := values[i].(type) {
			case []byte:
				rowData[col] = string(v)
			default:
				rowData[col] = v
			}
		}
		result = append(result, rowData)
		count++
	}

	c.JSON(http.StatusOK, gin.H{
		"columns": columns,
		"data":    result,
	})
}

func handleNonQuery(c *gin.Context, db *sql.DB, sqlStr string) {
	// Split multiple statements
	statements := strings.Split(sqlStr, ";")
	results := make([]map[string]interface{}, 0)

	for _, stmt := range statements {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}

		result, err := execStatement(db, stmt)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":      err.Error(),
				"statement":  stmt,
				"successful": results,
			})
			return
		}
		results = append(results, result)
	}

	c.JSON(http.StatusOK, gin.H{
		"results": results,
	})
}

func execStatement(db *sql.DB, stmt string) (map[string]interface{}, error) {
	res, err := db.Exec(stmt)
	if err != nil {
		return nil, err
	}

	result := make(map[string]interface{})
	result["statement"] = stmt

	if affected, err := res.RowsAffected(); err == nil {
		result["rows_affected"] = affected
	}

	if lastId, err := res.LastInsertId(); err == nil {
		result["last_insert_id"] = lastId
	}

	return result, nil
}

func handleDownload(c *gin.Context) {
	query := c.Query("sql")
	if !validateQuery(query, false) {
		c.String(http.StatusBadRequest, "Invalid query")
		return
	}

	rows, err := db.Query(query)
	if err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}
	defer func(rows *sql.Rows) {
		err := rows.Close()
		if err != nil {
			log.Fatal(err)
		}
	}(rows)

	c.Writer.Header().Set("Content-Type", "text/csv")
	c.Writer.Header().Set("Content-Disposition", "attachment; filename=export.csv")
	writer := csv.NewWriter(c.Writer)
	defer writer.Flush()

	columns, _ := rows.Columns()
	_ = writer.Write(columns)

	values := make([]interface{}, len(columns))
	scanArgs := make([]interface{}, len(columns))
	for i := range values {
		scanArgs[i] = &values[i]
	}

	for rows.Next() {
		err = rows.Scan(scanArgs...)
		if err != nil {
			log.Println(err)
			return
		}

		record := make([]string, len(columns))
		for i, v := range values {
			if v == nil {
				record[i] = ""
			} else {
				// Convert values to string explicitly
				switch v := v.(type) {
				case []byte:
					record[i] = string(v) // Convert []byte to string
				case string:
					record[i] = v
				case int, int64, float64, bool:
					record[i] = fmt.Sprintf("%v", v)
				default:
					record[i] = fmt.Sprintf("%v", v) // Fallback to string representation
				}
			}
		}

		_ = writer.Write(record)
	}
}
