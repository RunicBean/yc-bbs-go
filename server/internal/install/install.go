package install

import (
	"bbs-go/internal/models"
	"bbs-go/internal/models/constants"
	"bbs-go/internal/pkg/config"
	"bbs-go/internal/pkg/iplocator"
	"bbs-go/internal/pkg/search"
	"bbs-go/internal/scheduler"
	"bbs-go/internal/services"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/mlogclub/simple/common/dates"
	"github.com/mlogclub/simple/common/passwd"
	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"

	"github.com/golang-migrate/migrate/v4"
	m "github.com/golang-migrate/migrate/v4/database/mysql"
	_ "github.com/golang-migrate/migrate/v4/source/file"

	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
)

var (
	installMx sync.Mutex
)

// 测试数据库连接
type DbConfigReq struct {
	Host     string `json:"host"`
	Port     string `json:"port"`
	Database string `json:"database"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// 执行安装
type InstallReq struct {
	SiteTitle       string      `json:"siteTitle"`
	SiteDescription string      `json:"siteDescription"`
	DbConfig        DbConfigReq `json:"dbConfig"`
	Username        string      `json:"username"`
	Password        string      `json:"password"`
}

func (r DbConfigReq) GetConnStr() string {
	return fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable", r.Host, r.Port, r.Username, r.Password, r.Database)
	// return r.Username + ":" + r.Password + "@tcp(" + r.Host + ":" + r.Port + ")/" + r.Database + "?charset=utf8mb4&parseTime=True&multiStatements=true&loc=Local"
}

func TestDbConnection(req DbConfigReq) error {
	// 尝试连接数据库
	db, err := gorm.Open(postgres.Open(req.GetConnStr()))
	if err != nil {
		return err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return err
	}

	if err = sqlDB.Ping(); err != nil {
		return err
	}

	// 检查数据库中是否存在其他表
	type PgTable struct {
		SchemaName string `gorm:"column:schemaname"`
		TableName  string `gorm:"column:tablename"`
		// 其他列...
	}
	var tables []PgTable
	if err := db.Raw("SELECT * FROM pg_tables WHERE tablename NOT LIKE 'pg%' AND tablename NOT LIKE 'sql_%'").Scan(&tables).Error; err != nil {
		return err
	}

	if len(tables) > 0 {
		return errors.New("请使用空数据库进行安装")
	}

	return nil
}

func Install(req InstallReq) error {
	installMx.Lock()
	defer installMx.Unlock()

	if err := TestDbConnection(req.DbConfig); err != nil {
		return err
	}
	if err := WriteConfig(req); err != nil {
		return err
	}
	if err := InitDB(); err != nil {
		return err
	}
	if err := InitData(req); err != nil {
		return err
	}
	if err := InitOthers(); err != nil {
		return err
	}
	if err := WriteInstallSuccess(); err != nil {
		return err
	}
	return nil
}

func WriteConfig(req InstallReq) error {
	cfg := config.Instance
	cfg.DB.Url = req.DbConfig.GetConnStr()
	return config.WriteConfig(cfg)
}

func WriteInstallSuccess() error {
	cfg := config.Instance
	cfg.Installed = true
	return config.WriteConfig(cfg)
}

func InitConfig() {
	cfg, _, err := config.ReadConfig()
	if err != nil {
		panic(err)
	}
	config.Instance = cfg
}

func InitDB() error {
	conf := config.Instance.DB
	db, err := gorm.Open(mysql.Open(conf.Url), &gorm.Config{
		NamingStrategy: schema.NamingStrategy{
			TablePrefix:   "t_",
			SingularTable: true,
		},
		Logger: logger.New(log.New(os.Stdout, "", log.LstdFlags), logger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  logger.Info,
			IgnoreRecordNotFoundError: false,
			Colorful:                  true,
		}),
	})

	if err != nil {
		slog.Error(err.Error())
		return err
	}

	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxIdleConns(conf.MaxIdleConns)
		sqlDB.SetMaxOpenConns(conf.MaxOpenConns)
		sqlDB.SetConnMaxIdleTime(time.Duration(conf.ConnMaxIdleTimeSeconds) * time.Second)
		sqlDB.SetConnMaxLifetime(time.Duration(conf.ConnMaxLifetimeSeconds) * time.Second)
	}

	// migrate
	if err := db.AutoMigrate(models.Models...); nil != err {
		slog.Error("auto migrate tables failed", slog.Any("error", err))
		return err
	}

	// run migrations
	if err := runMigrations(db); err != nil {
		slog.Error(err.Error())
		return err
	}

	sqls.SetDB(db)
	return nil
}

func runMigrations(db *gorm.DB) error {
	s, _ := db.DB()
	driver, err := m.WithInstance(s, &m.Config{})
	if err != nil {
		return err
	}

	m, err := migrate.NewWithDatabaseInstance(
		"file://migrations",
		"mysql",
		driver,
	)
	if err != nil {
		return err
	}

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return err
	}

	return nil
}

func InitData(req InstallReq) error {
	// 初始化用户
	user := &models.User{
		Nickname:   req.Username,
		Username:   sqls.SqlNullString(req.Username),
		Password:   passwd.EncodePassword(req.Password),
		Status:     constants.StatusOk,
		CreateTime: dates.NowTimestamp(),
		UpdateTime: dates.NowTimestamp(),
	}
	if err := services.UserService.Create(user); err != nil {
		return err
	}

	// 初始化用户角色
	role := services.RoleService.GetByCode(constants.RoleOwner)
	if role == nil {
		return errors.New("请先配置超级管理员")
	}
	services.UserRoleService.UpdateUserRoles(user.Id, []int64{role.Id})

	// 初始化系统配置
	services.SysConfigService.Set(constants.SysConfigSiteTitle, req.SiteTitle, "站点标题", "站点标题")
	services.SysConfigService.Set(constants.SysConfigSiteDescription, req.SiteDescription, "站点描述", "站点描述")

	return nil
}

func InitOthers() error {
	if config.IsProd() {
		scheduler.Start()
	}
	iplocator.InitIpLocator()
	search.Init()
	return nil
}
