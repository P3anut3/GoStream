package config

type Config struct {
	HTTP     HTTPConfig     `yaml:"http"`
	Storage  StorageConfig  `yaml:"storage"`
	Redis    RedisConfig    `yaml:"redis"`
	Database DatabaseConfig `yaml:"-"`
	Auth     AuthConfig     `yaml:"-"`
}

type HTTPConfig struct {
	Addr string `yaml:"addr"`
}

type StorageConfig struct {
	VideoDir string `yaml:"video_dir"`
	CoverDir string `yaml:"cover_dir"`
}

// RedisConfig 是可选的缓存连接：Addr 为空表示不启用缓存，业务直接读写 MySQL。
// 密码只从环境变量读取，不能写进 YAML。
type RedisConfig struct {
	Addr     string `yaml:"addr"`
	Password string `yaml:"-"`
}

type DatabaseConfig struct {
	DSN string
}

type AuthConfig struct {
	JWTSecret        string
	FeedCursorSecret string
}
