// Command perfseed 为 NFR-01（20 账号 / 50 KB / 200 资料）与 NFR-02（5,000 切块 + 图谱关系）生成确定性压测数据集。
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"golang.org/x/crypto/bcrypt"

	"yanzhi/server/internal/platform/db"
	"yanzhi/server/internal/textutil"
)

type manifest struct {
	Password string       `json:"password"`
	Users    []userSeed   `json:"users"`
	MainUser string       `json:"main_user_id"`
	KBIDs    []string     `json:"kb_ids"`
	DocCount int          `json:"doc_count"`
	Chunks   int          `json:"chunk_count"`
	Prompt   string       `json:"sample_question"`
}

type userSeed struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

func main() {
	dbURL := flag.String("database-url", os.Getenv("DATABASE_URL"), "PostgreSQL connection string")
	migrationsDir := flag.String("migrations", filepath.Join("migrations"), "Migrations directory")
	cost := flag.Int("bcrypt-cost", 10, "Bcrypt cost for seeded users")
	password := flag.String("password", "PerfPass123", "Shared password for 20 seeded accounts")
	outPath := flag.String("out", filepath.Join("..", "perf", "seed_manifest.json"), "Output manifest JSON path")
	flag.Parse()

	if strings.TrimSpace(*dbURL) == "" {
		log.Fatal("DATABASE_URL or --database-url is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if err := db.Migrate(ctx, *dbURL, *migrationsDir); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(ctx, *dbURL)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	var dim int
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(NULLIF(atttypmod, -1), 1024)
		FROM pg_attribute WHERE attrelid = 'chunks'::regclass AND attname = 'embedding'`).Scan(&dim); err != nil {
		log.Fatalf("inspect vector dim: %v", err)
	}

	var gen int64
	err = pool.QueryRow(ctx, `SELECT id FROM embedding_generations WHERE active LIMIT 1`).Scan(&gen)
	if err != nil {
		err = pool.QueryRow(ctx, `INSERT INTO embedding_generations (model_name, dimension, active) VALUES ('perf-seed', $1, true) RETURNING id`, dim).Scan(&gen)
		if err != nil {
			log.Fatalf("seed generation: %v", err)
		}
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(*password), *cost)
	if err != nil {
		log.Fatalf("bcrypt: %v", err)
	}

	users := make([]userSeed, 0, 20)
	for i := 0; i < 20; i++ {
		email := fmt.Sprintf("perf-user-%02d@example.edu.cn", i+1)
		uid := uuid.NewString()
		_, err := pool.Exec(ctx, `
			INSERT INTO users (id, email, name, password_hash, role, email_verified_at, storage_bytes, session_version, created_at)
			VALUES ($1::uuid, $2, $3, $4, 'user', now(), 0, 1, now())
			ON CONFLICT (email) DO UPDATE SET password_hash = EXCLUDED.password_hash`,
			uid, email, fmt.Sprintf("压测用户%02d", i+1), string(hash))
		if err != nil {
			log.Fatalf("upsert user %s: %v", email, err)
		}
		if err := pool.QueryRow(ctx, `SELECT id::text FROM users WHERE email = $1`, email).Scan(&uid); err != nil {
			log.Fatalf("load user %s: %v", email, err)
		}
		users = append(users, userSeed{ID: uid, Email: email})
	}

	mainUID := users[0].ID
	// 清理该压测主账号旧压测数据，保持幂等
	_, _ = pool.Exec(ctx, `DELETE FROM kg_relations WHERE owner_id = $1::uuid`, mainUID)
	_, _ = pool.Exec(ctx, `DELETE FROM kg_entities WHERE owner_id = $1::uuid`, mainUID)
	_, _ = pool.Exec(ctx, `DELETE FROM documents WHERE owner_id = $1::uuid`, mainUID)
	_, _ = pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE owner_id = $1::uuid`, mainUID)
	_, _ = pool.Exec(ctx, `DELETE FROM term_stats WHERE owner_id = $1::uuid`, mainUID)

	kbIDs := make([]string, 0, 50)
	for k := 0; k < 50; k++ {
		kbID := uuid.NewString()
		_, err := pool.Exec(ctx, `
			INSERT INTO knowledge_bases (id, owner_id, name, graph_enabled, created_at, updated_at)
			VALUES ($1::uuid, $2::uuid, $3, true, now(), now())`,
			kbID, mainUID, fmt.Sprintf("压测知识库-%02d", k+1))
		if err != nil {
			log.Fatalf("insert kb %d: %v", k, err)
		}
		kbIDs = append(kbIDs, kbID)
	}

	const totalDocs = 200
	const chunksPerDoc = 25 // 200 * 25 = 5,000 切块
	termDF := map[string]int{}
	var firstDocID, firstChunkID, secondChunkID string

	tx, err := pool.Begin(ctx)
	if err != nil {
		log.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	for d := 0; d < totalDocs; d++ {
		docID := uuid.NewString()
		kbID := kbIDs[d%len(kbIDs)]
		sum := sha256.Sum256([]byte(fmt.Sprintf("perf-doc-%d-%d", d, time.Now().UnixNano())))
		docHash := hex.EncodeToString(sum[:])
		title := fmt.Sprintf("Perf Paper %03d on LoRA and ZephyrBench", d+1)
		if d == 0 {
			firstDocID = docID
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO documents (id, owner_id, kb_id, source_type, title, sha256, status, graph_status, page_count, has_physical_pages, embedding_generation, created_at, updated_at)
			VALUES ($1::uuid, $2::uuid, $3::uuid, 'pdf', $4, $5, 'ready', 'ready', 25, true, $6, now(), now())`,
			docID, mainUID, kbID, title, docHash, gen)
		if err != nil {
			log.Fatalf("insert doc %d: %v", d, err)
		}

		for c := 0; c < chunksPerDoc; c++ {
			chunkID := uuid.NewString()
			if d == 0 && c == 0 {
				firstChunkID = chunkID
			}
			if d == 0 && c == 1 {
				secondChunkID = chunkID
			}
			text := fmt.Sprintf("Document %03d paragraph %02d studies LoRA adaptation and ZephyrBench evaluation on page %d.", d+1, c+1, c+1)
			terms := textutil.Terms(text)
			vec := deterministicVec(dim, d*chunksPerDoc+c)
			_, err := tx.Exec(ctx, `
				INSERT INTO chunks (id, document_id, kind, page_start, paragraph_index, overlap_prefix, text, version_no, char_count, term_count, embedding, embedding_generation, created_at, updated_at)
				VALUES ($1::uuid, $2::uuid, 'paragraph', $3, $4, '', $5, 1, $6, $7, $8, $9, now(), now())`,
				chunkID, docID, c+1, c, text, len([]rune(text)), len(terms), vec, gen)
			if err != nil {
				log.Fatalf("insert chunk %d-%d: %v", d, c, err)
			}
			tf := map[string]int{}
			for _, term := range terms {
				tf[term]++
			}
			for term, n := range tf {
				termDF[term]++
				_, err := tx.Exec(ctx, `INSERT INTO chunk_postings (chunk_id, term, field, tf) VALUES ($1::uuid, $2, 'text', $3)`,
					chunkID, term, n)
				if err != nil {
					log.Fatalf("insert posting: %v", err)
				}
			}
		}
	}

	for term, df := range termDF {
		if _, err := tx.Exec(ctx, `INSERT INTO term_stats (owner_id, term, df) VALUES ($1::uuid, $2, $3)`, mainUID, term, df); err != nil {
			log.Fatalf("insert term_stats: %v", err)
		}
	}

	// Seed 1-hop KG relation linking first two chunks for NFR-02 graph supplement coverage
	paperEnt := uuid.NewString()
	methodEnt := uuid.NewString()
	relID := uuid.NewString()
	_, _ = tx.Exec(ctx, `
		INSERT INTO kg_entities (id, owner_id, type, name, merge_key, document_id, created_at)
		VALUES ($1::uuid, $2::uuid, 'paper', 'Perf Paper 001', $3, $4::uuid, now()),
		       ($5::uuid, $2::uuid, 'method', 'LoRA', 'method:lora', NULL, now())`,
		paperEnt, mainUID, "paper:"+firstDocID, firstDocID, methodEnt)
	_, _ = tx.Exec(ctx, `
		INSERT INTO kg_relations (id, owner_id, type, source_id, target_id, suspended, created_at)
		VALUES ($1::uuid, $2::uuid, 'proposes', $3::uuid, $4::uuid, false, now())`,
		relID, mainUID, paperEnt, methodEnt)
	_, _ = tx.Exec(ctx, `
		INSERT INTO kg_evidence (id, relation_id, document_id, chunk_id, page_start, sentence)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 1, 'LoRA adaptation'),
		       ($5::uuid, $2::uuid, $3::uuid, $6::uuid, 2, 'ZephyrBench evaluation')`,
		uuid.NewString(), relID, firstDocID, firstChunkID, uuid.NewString(), secondChunkID)

	if err := tx.Commit(ctx); err != nil {
		log.Fatalf("commit: %v", err)
	}

	m := manifest{
		Password: *password,
		Users:    users,
		MainUser: mainUID,
		KBIDs:    kbIDs,
		DocCount: totalDocs,
		Chunks:   totalDocs * chunksPerDoc,
		Prompt:   "LoRA adaptation and ZephyrBench",
	}
	if err := os.MkdirAll(filepath.Dir(*outPath), 0o755); err != nil {
		log.Fatalf("mkdir manifest dir: %v", err)
	}
	raw, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(*outPath, raw, 0o600); err != nil {
		log.Fatalf("write manifest: %v", err)
	}
	fmt.Printf("seeded 20 users, 50 KBs, %d docs, %d chunks -> %s\n", totalDocs, totalDocs*chunksPerDoc, *outPath)
}

func deterministicVec(dim, seed int) pgvector.Vector {
	v := make([]float32, dim)
	var sum float64
	for i := 0; i < dim; i++ {
		val := math.Sin(float64(seed*31+i+1)) * 0.5
		v[i] = float32(val)
		sum += val * val
	}
	norm := math.Sqrt(sum)
	if norm > 0 {
		for i := range v {
			v[i] = float32(float64(v[i]) / norm)
		}
	}
	return pgvector.NewVector(v)
}
