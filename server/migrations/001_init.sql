CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE users (
    id uuid PRIMARY KEY,
    email text NOT NULL UNIQUE,
    name text NOT NULL,
    password_hash text NOT NULL,
    role text NOT NULL CHECK (role IN ('user', 'admin')),
    email_verified_at timestamptz,
    tokens_invalid_before timestamptz,
    storage_bytes bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL
);

CREATE TABLE revoked_tokens (
    jti uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL
);

CREATE TABLE knowledge_bases (
    id uuid PRIMARY KEY,
    owner_id uuid NOT NULL REFERENCES users (id),
    name text NOT NULL,
    answer_instruction text,
    retrieval_threshold numeric(4, 3),
    graph_enabled boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE INDEX knowledge_bases_owner_created ON knowledge_bases (owner_id, created_at);

CREATE TABLE documents (
    id uuid PRIMARY KEY,
    owner_id uuid NOT NULL REFERENCES users (id),
    kb_id uuid NOT NULL REFERENCES knowledge_bases (id),
    source_type text NOT NULL,
    title text NOT NULL,
    authors jsonb NOT NULL DEFAULT '[]',
    year integer,
    venue text,
    volume text,
    issue text,
    pages text,
    doi text,
    extra_meta jsonb NOT NULL DEFAULT '{}',
    status text NOT NULL CHECK (status IN ('pending', 'parsing', 'embedding', 'ready', 'failed')),
    failure_reason text,
    graph_status text CHECK (graph_status IS NULL OR graph_status IN ('pending', 'extracting', 'ready', 'failed')),
    graph_failure_reason text,
    graph_manual_retry_count integer NOT NULL DEFAULT 0,
    sha256 char(64),
    size_bytes bigint NOT NULL DEFAULT 0,
    page_count integer,
    has_physical_pages boolean NOT NULL DEFAULT true,
    skipped_pages integer[] NOT NULL DEFAULT '{}',
    relative_path text,
    storage_key text,
    source_url text,
    manual_retry_count integer NOT NULL DEFAULT 0,
    cancel_requested boolean NOT NULL DEFAULT false,
    embedding_generation bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE UNIQUE INDEX documents_owner_sha ON documents (owner_id, sha256) WHERE sha256 IS NOT NULL;
CREATE INDEX documents_owner_status ON documents (owner_id, status);
CREATE INDEX documents_owner_kb_created ON documents (owner_id, kb_id, created_at);
CREATE INDEX documents_title_trgm ON documents USING gin (lower(title) gin_trgm_ops);

CREATE TABLE parent_chunks (
    id uuid PRIMARY KEY,
    document_id uuid NOT NULL REFERENCES documents (id) ON DELETE CASCADE,
    position integer NOT NULL,
    page_start integer NOT NULL,
    text text NOT NULL
);

CREATE TABLE chunks (
    id uuid PRIMARY KEY,
    document_id uuid NOT NULL REFERENCES documents (id) ON DELETE CASCADE,
    parent_id uuid NOT NULL REFERENCES parent_chunks (id) ON DELETE CASCADE,
    kind text NOT NULL,
    page_start integer NOT NULL,
    paragraph_index integer NOT NULL,
    overlap_prefix integer NOT NULL DEFAULT 0,
    text text NOT NULL,
    version_no integer NOT NULL DEFAULT 1,
    char_count integer NOT NULL,
    term_count integer NOT NULL,
    embedding vector(1024),
    embedding_generation bigint NOT NULL,
    prev_chunk_id uuid,
    next_chunk_id uuid,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE INDEX chunks_document_paragraph ON chunks (document_id, paragraph_index);
CREATE INDEX chunks_parent ON chunks (parent_id);

CREATE TABLE chunk_postings (
    chunk_id uuid NOT NULL REFERENCES chunks (id) ON DELETE CASCADE,
    term text NOT NULL,
    field text NOT NULL CHECK (field IN ('text', 'question')),
    tf integer NOT NULL,
    PRIMARY KEY (chunk_id, term, field)
);
CREATE INDEX chunk_postings_term ON chunk_postings (term, chunk_id);

CREATE TABLE term_stats (
    owner_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    term text NOT NULL,
    df integer NOT NULL,
    PRIMARY KEY (owner_id, term)
);

CREATE TABLE tags (
    id uuid PRIMARY KEY,
    owner_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name text NOT NULL,
    created_at timestamptz NOT NULL,
    UNIQUE (owner_id, name)
);

CREATE TABLE document_tags (
    document_id uuid NOT NULL REFERENCES documents (id) ON DELETE CASCADE,
    tag_id uuid NOT NULL REFERENCES tags (id) ON DELETE CASCADE,
    PRIMARY KEY (document_id, tag_id)
);

CREATE TABLE jobs (
    id uuid PRIMARY KEY,
    kind text NOT NULL,
    owner_id uuid REFERENCES users (id),
    target_id uuid NOT NULL,
    request_id uuid NOT NULL,
    priority integer NOT NULL DEFAULT 100,
    attempt integer NOT NULL,
    status text NOT NULL CHECK (status IN ('pending', 'running', 'succeeded', 'failed', 'cancelled')),
    heartbeat_at timestamptz,
    error_message text,
    created_at timestamptz NOT NULL,
    started_at timestamptz,
    finished_at timestamptz
);
CREATE UNIQUE INDEX jobs_one_active ON jobs (kind, target_id) WHERE status IN ('pending', 'running');
CREATE INDEX jobs_claim ON jobs (status, priority, created_at);

CREATE TABLE job_stages (
    id uuid PRIMARY KEY,
    job_id uuid NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    name text NOT NULL,
    status text NOT NULL CHECK (status IN ('pending', 'running', 'succeeded', 'failed', 'skipped')),
    detail jsonb,
    started_at timestamptz,
    finished_at timestamptz,
    UNIQUE (job_id, name)
);

CREATE TABLE kg_entities (
    id uuid PRIMARY KEY,
    owner_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    type text NOT NULL,
    name text NOT NULL,
    merge_key text NOT NULL,
    document_id uuid REFERENCES documents (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL,
    UNIQUE (owner_id, merge_key)
);

CREATE TABLE kg_relations (
    id uuid PRIMARY KEY,
    owner_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    type text NOT NULL,
    source_id uuid NOT NULL REFERENCES kg_entities (id) ON DELETE CASCADE,
    target_id uuid NOT NULL REFERENCES kg_entities (id) ON DELETE CASCADE,
    suspended boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL,
    UNIQUE (owner_id, type, source_id, target_id)
);

CREATE TABLE kg_evidence (
    id uuid PRIMARY KEY,
    relation_id uuid NOT NULL REFERENCES kg_relations (id) ON DELETE CASCADE,
    document_id uuid NOT NULL REFERENCES documents (id) ON DELETE CASCADE,
    chunk_id uuid NOT NULL REFERENCES chunks (id) ON DELETE CASCADE,
    chunk_version integer NOT NULL,
    page_start integer NOT NULL,
    sentence text NOT NULL,
    created_at timestamptz NOT NULL
);
CREATE INDEX kg_evidence_chunk ON kg_evidence (chunk_id);
CREATE INDEX kg_evidence_relation ON kg_evidence (relation_id);
CREATE INDEX kg_evidence_document ON kg_evidence (document_id);

CREATE TABLE kg_unresolved_refs (
    id uuid PRIMARY KEY,
    document_id uuid NOT NULL REFERENCES documents (id) ON DELETE CASCADE,
    title text NOT NULL,
    created_at timestamptz NOT NULL
);

CREATE TABLE model_configs (
    kind text PRIMARY KEY CHECK (kind IN ('chat', 'embedding', 'rerank', 'vision')),
    base_url text NOT NULL,
    model_name text NOT NULL,
    api_key_nonce bytea NOT NULL,
    api_key_ciphertext bytea NOT NULL,
    api_key_last4 char(4) NOT NULL,
    key_version integer NOT NULL DEFAULT 1,
    dimension integer,
    updated_at timestamptz NOT NULL
);

CREATE TABLE embedding_generations (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    model_name text NOT NULL,
    dimension integer NOT NULL,
    active boolean NOT NULL
);
CREATE UNIQUE INDEX embedding_generations_one_active ON embedding_generations (active) WHERE active;

CREATE TABLE qa_sessions (
    id uuid PRIMARY KEY,
    owner_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    title text NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE INDEX qa_sessions_owner_updated ON qa_sessions (owner_id, updated_at DESC);

CREATE TABLE qa_turns (
    id uuid PRIMARY KEY,
    session_id uuid NOT NULL REFERENCES qa_sessions (id) ON DELETE CASCADE,
    question text NOT NULL,
    answer text NOT NULL,
    answer_kind text NOT NULL,
    rewritten boolean NOT NULL,
    reranked boolean NOT NULL,
    web_search boolean NOT NULL,
    scope jsonb NOT NULL,
    candidate_ids uuid[] NOT NULL DEFAULT '{}',
    graph_extra_ids uuid[] NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL
);

CREATE TABLE qa_citations (
    id uuid PRIMARY KEY,
    turn_id uuid NOT NULL REFERENCES qa_turns (id) ON DELETE CASCADE,
    chunk_id uuid REFERENCES chunks (id) ON DELETE SET NULL,
    document_id uuid,
    title_snapshot text NOT NULL,
    page_start integer,
    origin text NOT NULL,
    graph_supplement boolean NOT NULL DEFAULT false,
    label text NOT NULL,
    external_url text
);

CREATE TABLE model_call_logs (
    id uuid PRIMARY KEY,
    request_id uuid NOT NULL,
    user_id uuid,
    kind text NOT NULL,
    model text NOT NULL,
    elapsed_ms integer NOT NULL,
    prompt_tokens integer NOT NULL,
    completion_tokens integer NOT NULL,
    status text NOT NULL,
    prompt_sha256 char(64),
    created_at timestamptz NOT NULL
);
CREATE INDEX model_call_logs_request ON model_call_logs (request_id);

CREATE TABLE request_logs (
    id uuid PRIMARY KEY,
    request_id uuid NOT NULL,
    user_id uuid,
    method text NOT NULL,
    path text NOT NULL,
    status integer NOT NULL,
    elapsed_ms integer NOT NULL,
    created_at timestamptz NOT NULL
);

CREATE TABLE idempotency_keys (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    key text NOT NULL,
    request_hash char(64) NOT NULL,
    document_id uuid REFERENCES documents (id) ON DELETE SET NULL,
    status_code integer NOT NULL,
    created_at timestamptz NOT NULL,
    UNIQUE (user_id, key)
);
