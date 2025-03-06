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
	_ "github.com/go-sql-driver/mysql"
)

type ConfigInit struct {
	Path string `yaml:"path"`
	Port string `yaml:"port"`
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

//go:embed static/*
var staticFiles embed.FS

func main() {
	// 读取配置文件
	configInit := loadConfigInit()
	config := loadConfig(configInit.Path)

	// 转换JDBC URL为Go格式
	goDSN := convertJdbcUrl(config.Spring.Datasource.URL)
	goDSN = fmt.Sprintf("%s:%s@%s",
		config.Spring.Datasource.Username,
		config.Spring.Datasource.Password,
		goDSN)

	// 连接数据库
	var err error
	db, err = sql.Open("mysql", goDSN)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

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

	r.POST("/query", handleQuery)
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
func loadConfigInit() *ConfigInit {
	data, err := os.ReadFile("config.yaml")
	if err != nil {
		log.Fatal(err)
	}

	var configInit ConfigInit
	if err := yaml.Unmarshal(data, &configInit); err != nil {
		log.Fatal(err)
	}

	return &configInit
}

func convertJdbcUrl(jdbcUrl string) string {
	re := regexp.MustCompile(`jdbc:([^:]+)://([^/]+)/([^?]+)`)
	matches := re.FindStringSubmatch(jdbcUrl)
	if len(matches) != 4 {
		log.Fatal("Invalid JDBC URL format")
	}

	return fmt.Sprintf("tcp(%s)/%s", matches[2], matches[3])
}

func validateQuery(sql string) bool {
	matched, _ := regexp.MatchString(`^SELECT\s+.*`, strings.ToUpper(sql))
	return matched && !strings.Contains(sql, ";")
}

func handleQuery(c *gin.Context) {
	type Request struct {
		SQL string `json:"sql"`
	}

	var req Request
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}

	if !validateQuery(req.SQL) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Only SELECT queries are allowed"})
		return
	}

	// 执行查询
	rows, err := db.Query(req.SQL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	columns, _ := rows.Columns()
	result := make([]map[string]interface{}, 0)

	// 读取前5条记录
	count := 0
	for rows.Next() && count < 5 {
		values := make([]interface{}, len(columns))
		scanArgs := make([]interface{}, len(columns))
		for i := range values {
			scanArgs[i] = &values[i]
		}

		err = rows.Scan(scanArgs...)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		rowData := make(map[string]interface{})
		for i, col := range columns {
			// Convert values to string explicitly
			switch v := values[i].(type) {
			case []byte:
				rowData[col] = string(v) // Convert []byte to string
			case string:
				rowData[col] = v
			case int, int64, float64, bool:
				rowData[col] = v
			case nil:
				rowData[col] = nil
			default:
				rowData[col] = fmt.Sprintf("%v", v) // Fallback to string representation
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

func handleDownload(c *gin.Context) {
	sql := c.Query("sql")
	if !validateQuery(sql) {
		c.String(http.StatusBadRequest, "Invalid query")
		return
	}

	rows, err := db.Query(sql)
	if err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	c.Writer.Header().Set("Content-Type", "text/csv")
	c.Writer.Header().Set("Content-Disposition", "attachment; filename=export.csv")
	writer := csv.NewWriter(c.Writer)
	defer writer.Flush()

	columns, _ := rows.Columns()
	writer.Write(columns)

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

		writer.Write(record)
	}
}
