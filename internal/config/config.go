package config

type Config struct {
	HTTP     HTTPConfig     `yaml:"http"`
	Storage  StorageConfig  `yaml:"storage"`
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

type DatabaseConfig struct {
	DSN string
}

type AuthConfig struct {
	JWTSecret        string
	FeedCursorSecret string
}
