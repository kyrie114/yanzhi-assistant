package config

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	DatabaseURL    string
	HTTPAddr       string
	JWTSecret      []byte
	AESKey         []byte
	DocreaderAddr  string
	DocreaderToken string
	DataDir        string
	MigrationsDir  string
	BcryptCost     int
	EmailRestrict  bool
	ChunkMin       int
	ChunkMax       int
	ChunkOverlap   int
	ParentMin      int
	ParentMax      int
	TopK           int
	RRFK           int
	BM25K1         float64
	BM25B          float64
	Threshold      float64
	EmbedDim       int
	IngestWorkers  int
	GraphWorkers   int
	EmbedBatch     int
	ModelTimeout   time.Duration
	DocTimeout     time.Duration
	CORSOrigin     string
}

func Load() (Config, error) {
	cfg := Config{
		DatabaseURL:    os.Getenv("DATABASE_URL"),
		HTTPAddr:       env("HTTP_ADDR", ":8080"),
		DocreaderAddr:  os.Getenv("DOCREADER_ADDR"),
		DocreaderToken: os.Getenv("DOCREADER_TOKEN"),
		DataDir:        env("DATA_DIR", "data"),
		MigrationsDir:  env("MIGRATIONS_DIR", "migrations"),
		BcryptCost:     envInt("BCRYPT_COST", 10),
		EmailRestrict:  os.Getenv("EMAIL_VERIFICATION_REQUIRED") == "true",
		ChunkMin:       envInt("CHUNK_MIN", 500),
		ChunkMax:       envInt("CHUNK_MAX", 800),
		ChunkOverlap:   envInt("CHUNK_OVERLAP", 100),
		ParentMin:      envInt("PARENT_MIN", 2000),
		ParentMax:      envInt("PARENT_MAX", 3000),
		TopK:           envInt("TOP_K", 5),
		RRFK:           envInt("RRF_K", 60),
		BM25K1:         envFloat("BM25_K1", 1.2),
		BM25B:          envFloat("BM25_B", 0.75),
		Threshold:      envFloat("DEFAULT_THRESHOLD", 0.3),
		EmbedDim:       envInt("EMBEDDING_DIM", 1024),
		IngestWorkers:  envInt("INGEST_WORKERS", 2),
		GraphWorkers:   envInt("GRAPH_WORKERS", 2),
		EmbedBatch:     envInt("EMBED_BATCH_SIZE", 16),
		ModelTimeout:   time.Duration(envInt("MODEL_TIMEOUT_SECONDS", 60)) * time.Second,
		DocTimeout:     time.Duration(envInt("DOCREADER_TIMEOUT_SECONDS", 120)) * time.Second,
		CORSOrigin:     env("CORS_ORIGIN", "http://localhost:5173"),
	}
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		return cfg, fmt.Errorf("JWT_SECRET is required")
	}
	cfg.JWTSecret = []byte(secret)
	key, err := decodeKey(os.Getenv("AES_KEY"))
	if err != nil {
		return cfg, err
	}
	cfg.AESKey = key
	if cfg.DatabaseURL == "" {
		return cfg, fmt.Errorf("DATABASE_URL is required")
	}
	return cfg, nil
}

func decodeKey(raw string) ([]byte, error) {
	if raw == "" {
		return nil, fmt.Errorf("AES_KEY is required")
	}
	if b, err := hex.DecodeString(raw); err == nil && len(b) == 32 {
		return b, nil
	}
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(b) != 32 {
		return nil, fmt.Errorf("AES_KEY must be 32 bytes, hex or base64")
	}
	return b, nil
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func envInt(name string, fallback int) int {
	v := os.Getenv(name)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func envFloat(name string, fallback float64) float64 {
	v := os.Getenv(name)
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return fallback
	}
	return n
}
