CREATE TABLE IF NOT EXISTS page_translations (
    id uuid PRIMARY KEY,
    document_id uuid NOT NULL REFERENCES documents (id) ON DELETE CASCADE,
    owner_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    page_no integer NOT NULL CHECK (page_no >= 1),
    source_sha256 text NOT NULL,
    translation text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS page_translations_doc_page
    ON page_translations (document_id, page_no);

CREATE INDEX IF NOT EXISTS page_translations_owner_doc
    ON page_translations (owner_id, document_id, page_no);
