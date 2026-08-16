package projection

import (
	"database/sql"
	"fmt"
	"os"

	_ "github.com/lib/pq"
	"gopkg.in/yaml.v3"
)

type DBConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	DBName   string
}

// configFile mirrors the database section of the mandyville config.yaml.
type configFile struct {
	Database struct {
		ReadUser  string `yaml:"read_user"`
		WriteUser string `yaml:"write_user"`
		DB        string `yaml:"db"`
		Host      string `yaml:"host"`
		Port      int    `yaml:"port"`
		Pass      string `yaml:"pass"`
	} `yaml:"database"`
}

// LoadDBConfigFromFile reads database connection details from a mandyville
// config.yaml file. It uses the read_user by default.
func LoadDBConfigFromFile(path string) (DBConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return DBConfig{}, fmt.Errorf("reading config file: %w", err)
	}

	var cf configFile
	if err := yaml.Unmarshal(data, &cf); err != nil {
		return DBConfig{}, fmt.Errorf("parsing config file: %w", err)
	}

	return DBConfig{
		Host:     cf.Database.Host,
		Port:     cf.Database.Port,
		User:     cf.Database.ReadUser,
		Password: cf.Database.Pass,
		DBName:   cf.Database.DB,
	}, nil
}

func OpenDB(cfg DBConfig) (*sql.DB, error) {
	connStr := fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
		cfg.Host, cfg.Port, cfg.User, cfg.Password, cfg.DBName,
	)
	return sql.Open("postgres", connStr)
}
