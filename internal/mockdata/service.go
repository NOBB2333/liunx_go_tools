package mockdata

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/brianvoe/gofakeit/v6"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// MockDataConfig mock数据生成配置
type MockDataConfig struct {
	DSN    string
	Tables []string
	Rows   int
	DryRun bool
}

type columnInfo struct {
	ColumnName    string `gorm:"column:COLUMN_NAME"`
	ColumnType    string `gorm:"column:COLUMN_TYPE"`
	IsNullable    string `gorm:"column:IS_NULLABLE"`
	ColumnComment string `gorm:"column:COLUMN_COMMENT"`
	ColumnKey     string `gorm:"column:COLUMN_KEY"`
	Extra         string `gorm:"column:EXTRA"`
}

// MockDataService 数据库mock数据生成服务
type MockDataService struct{}

var tableNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*$`)

// RunMockData 包级入口，供 main 调用
func RunMockData(dsn string, tables []string, rows int, dryRun bool) error {
	svc := NewMockDataService()
	return svc.Run(context.Background(), MockDataConfig{
		DSN:    dsn,
		Tables: tables,
		Rows:   rows,
		DryRun: dryRun,
	})
}

func NewMockDataService() *MockDataService {
	return &MockDataService{}
}

func (s *MockDataService) Run(ctx context.Context, cfg MockDataConfig) error {
	if strings.TrimSpace(cfg.DSN) == "" {
		return fmt.Errorf("database DSN is required")
	}
	if cfg.Rows <= 0 || cfg.Rows > 100000 {
		return fmt.Errorf("rows must be between 1 and 100000")
	}
	if len(cfg.Tables) == 0 {
		return fmt.Errorf("at least one table is required")
	}
	for _, table := range cfg.Tables {
		if !tableNamePattern.MatchString(strings.TrimSpace(table)) {
			return fmt.Errorf("invalid table name: %q", table)
		}
	}
	db, err := gorm.Open(mysql.Open(cfg.DSN), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return fmt.Errorf("连接数据库失败: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	for _, table := range cfg.Tables {
		table = strings.TrimSpace(table)
		if table == "" {
			continue
		}
		if err := s.mockTable(ctx, db, table, cfg.Rows, cfg.DryRun); err != nil {
			return fmt.Errorf("表 %s 生成失败: %w", table, err)
		}
		if cfg.DryRun {
			fmt.Printf("[dry-run] 表 %s 预览完成\n", table)
		} else {
			fmt.Printf("表 %s: 成功插入 %d 行\n", table, cfg.Rows)
		}
	}
	return nil
}

func (s *MockDataService) mockTable(ctx context.Context, db *gorm.DB, table string, rows int, dryRun bool) error {
	var cols []columnInfo
	err := db.WithContext(ctx).Raw(
		`SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLUMN_COMMENT, COLUMN_KEY, EXTRA
		 FROM information_schema.COLUMNS
		 WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?
		 ORDER BY ORDINAL_POSITION`, table,
	).Scan(&cols).Error
	if err != nil {
		return err
	}
	if len(cols) == 0 {
		return fmt.Errorf("表不存在或无字段")
	}

	const batchSize = 100
	batch := make([]map[string]interface{}, 0, batchSize)

	for i := 0; i < rows; i++ {
		row := buildMockRow(cols)
		if dryRun {
			fmt.Printf("  row %d: %v\n", i+1, row)
			continue
		}
		batch = append(batch, row)
		if len(batch) >= batchSize || i == rows-1 {
			if err := db.WithContext(ctx).Table(table).Create(&batch).Error; err != nil {
				return err
			}
			batch = batch[:0]
		}
	}
	return nil
}

func buildMockRow(cols []columnInfo) map[string]interface{} {
	row := make(map[string]interface{}, len(cols))
	for _, col := range cols {
		// 跳过自增主键
		if col.ColumnKey == "PRI" && strings.Contains(col.Extra, "auto_increment") {
			continue
		}
		row[col.ColumnName] = generateMockValue(col)
	}
	return row
}

func generateMockValue(col columnInfo) interface{} {
	// nullable 字段 10% 概率为 null
	if col.IsNullable == "YES" && gofakeit.IntRange(1, 10) == 1 {
		return nil
	}

	hint := strings.ToLower(col.ColumnName + " " + col.ColumnComment)
	colType := strings.ToLower(col.ColumnType)

	return inferMockValue(hint, colType)
}

func inferMockValue(hint, colType string) interface{} {
	switch {
	case containsAnyKw(hint, "phone", "手机", "电话", "mobile", "tel"):
		return gofakeit.Numerify("1##########")
	case containsAnyKw(hint, "email", "邮件", "邮箱"):
		return gofakeit.Email()
	case containsAnyKw(hint, "username", "用户名", "nickname", "昵称"):
		return gofakeit.Username()
	case containsAnyKw(hint, "name", "姓名", "名字"):
		return gofakeit.Name()
	case containsAnyKw(hint, "addr", "address", "地址"):
		return gofakeit.Address().Address
	case containsAnyKw(hint, "city", "城市"):
		return gofakeit.City()
	case containsAnyKw(hint, "company", "公司", "企业", "单位"):
		return gofakeit.Company()
	case containsAnyKw(hint, "url", "链接", "网址"):
		return gofakeit.URL()
	case containsAnyKw(hint, "ip"):
		return gofakeit.IPv4Address()
	case containsAnyKw(hint, "id_card", "身份证"):
		return gofakeit.Numerify("4403##########0###")
	case containsAnyKw(hint, "remark", "desc", "备注", "描述", "说明", "内容", "comment"):
		return gofakeit.Sentence(8)
	case containsAnyKw(hint, "amount", "金额", "price", "价格", "费用", "salary", "工资", "wage"):
		return gofakeit.Float64Range(100, 99999)
	case containsAnyKw(hint, "age", "年龄"):
		return gofakeit.IntRange(18, 70)
	case containsAnyKw(hint, "gender", "sex", "性别"):
		return gofakeit.IntRange(0, 1)
	case containsAnyKw(hint, "status", "状态"):
		return gofakeit.IntRange(1, 3)
	case containsAnyKw(hint, "type", "类型", "kind"):
		return gofakeit.IntRange(1, 5)
	case containsAnyKw(hint, "sort", "order", "排序", "序号"):
		return gofakeit.IntRange(1, 999)
	case containsAnyKw(hint, "create_time", "created_at", "create_date", "创建时间"):
		return gofakeit.DateRange(time.Now().AddDate(-1, 0, 0), time.Now()).Format("2006-01-02 15:04:05")
	case containsAnyKw(hint, "update_time", "updated_at", "update_date", "更新时间"):
		return time.Now().Format("2006-01-02 15:04:05")
	case containsAnyKw(hint, "date", "time", "日期", "时间"):
		return gofakeit.DateRange(time.Now().AddDate(-1, 0, 0), time.Now()).Format("2006-01-02 15:04:05")
	}

	return inferMockValueByType(colType)
}

func inferMockValueByType(colType string) interface{} {
	switch {
	case colType == "tinyint(1)" || colType == "bit(1)":
		return gofakeit.IntRange(0, 1)
	case strings.HasPrefix(colType, "tinyint"), strings.HasPrefix(colType, "smallint"):
		return gofakeit.IntRange(1, 100)
	case strings.HasPrefix(colType, "int"), strings.HasPrefix(colType, "bigint"), strings.HasPrefix(colType, "mediumint"):
		return gofakeit.IntRange(1, 99999)
	case strings.HasPrefix(colType, "decimal"), strings.HasPrefix(colType, "float"), strings.HasPrefix(colType, "double"):
		return gofakeit.Float64Range(0, 9999)
	case strings.HasPrefix(colType, "datetime"), strings.HasPrefix(colType, "timestamp"):
		return gofakeit.DateRange(time.Now().AddDate(-1, 0, 0), time.Now()).Format("2006-01-02 15:04:05")
	case strings.HasPrefix(colType, "date"):
		return gofakeit.DateRange(time.Now().AddDate(-1, 0, 0), time.Now()).Format("2006-01-02")
	case strings.HasPrefix(colType, "text"), strings.HasPrefix(colType, "longtext"), strings.HasPrefix(colType, "mediumtext"):
		return gofakeit.Paragraph(1, 2, 8, " ")
	case strings.HasPrefix(colType, "varchar"), strings.HasPrefix(colType, "char"):
		return gofakeit.Word()
	default:
		return gofakeit.Word()
	}
}

func containsAnyKw(s string, keywords ...string) bool {
	for _, kw := range keywords {
		if strings.Contains(s, kw) {
			return true
		}
	}
	return false
}
