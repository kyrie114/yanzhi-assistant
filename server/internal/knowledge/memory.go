package knowledge

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/identity"
	"yanzhi/server/internal/pdfcheck"
	"yanzhi/server/internal/platform/clock"
	"yanzhi/server/internal/platform/cryptobox"
)

type UserStorageCoordinator interface {
	GetStorageBytes(userID string) (int64, error)
	ReserveStorage(userID string, delta, quotaBytes int64) error
	ReleaseStorage(userID string, delta int64) error
}

type BlobStore interface {
	Put(key string, data []byte) error
	Get(key string) ([]byte, error)
	Delete(key string) error
}

type MemoryBlobStore struct {
	mu   sync.RWMutex
	data map[string][]byte
}

func NewMemoryBlobStore() *MemoryBlobStore {
	return &MemoryBlobStore{data: make(map[string][]byte)}
}

func (m *MemoryBlobStore) Put(key string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	m.data[key] = cp
	return nil
}

func (m *MemoryBlobStore) Get(key string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.data[key]
	if !ok {
		return nil, os.ErrNotExist
	}
	cp := make([]byte, len(b))
	copy(cp, b)
	return cp, nil
}

func (m *MemoryBlobStore) Delete(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

type kbRecord struct {
	ID                 string
	OwnerID            string
	Name               string
	GraphEnabled       bool
	AnswerInstruction  *string
	RetrievalThreshold *float64
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type docRecord struct {
	ID               string
	OwnerID          string
	KBID             string
	SourceType       string
	Title            string
	Authors          []string
	Year             *int
	Venue            *string
	Volume           *string
	Issue            *string
	Pages            *string
	DOI              *string
	PageCount        *int
	HasPhysicalPages *bool
	ManualRetryCount *int
	SizeBytes        int64
	StorageKey       string
	Status           string
	FailureReason    *string
	GraphStatus      *string
	SkippedPages     []int
	RelativePath     *string
	SHA256           string
	CancelRequested  bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type tagRecord struct {
	ID        string
	OwnerID   string
	Name      string
	CreatedAt time.Time
}

type idempotencyRecord struct {
	ID          string
	UserID      string
	Key         string
	RequestHash string
	DocID       string
	StatusCode  int
	CreatedAt   time.Time
}

type MemoryStore struct {
	mu              sync.RWMutex
	users           UserStorageCoordinator
	blobs           BlobStore
	clock           clock.Clock
	fallbackStorage map[string]int64

	kbs          map[string]kbRecord
	docs         map[string]docRecord
	shaIndex     map[string]map[string]string
	tags         map[string]tagRecord
	tagNameIndex map[string]map[string]string
	docTags      map[string]map[string]struct{}
	tagDocs      map[string]map[string]struct{}
	idempotency  map[string]idempotencyRecord
	parents      map[string]parentChunkRecord
	chunks       map[string]chunkRecord
	docChunks    map[string][]string
	docParents   map[string][]string
}

func NewMemoryStore(users UserStorageCoordinator, blobs BlobStore, clk clock.Clock) *MemoryStore {
	if clk == nil {
		clk = clock.System{}
	}
	if blobs == nil {
		blobs = NewMemoryBlobStore()
	}
	return &MemoryStore{
		users:           users,
		blobs:           blobs,
		clock:           clk,
		fallbackStorage: make(map[string]int64),
		kbs:             make(map[string]kbRecord),
		docs:            make(map[string]docRecord),
		shaIndex:        make(map[string]map[string]string),
		tags:            make(map[string]tagRecord),
		tagNameIndex:    make(map[string]map[string]string),
		docTags:         make(map[string]map[string]struct{}),
		tagDocs:         make(map[string]map[string]struct{}),
		idempotency:     make(map[string]idempotencyRecord),
		parents:         make(map[string]parentChunkRecord),
		chunks:          make(map[string]chunkRecord),
		docChunks:       make(map[string][]string),
		docParents:      make(map[string][]string),
	}
}

func (m *MemoryStore) getStorageBytes(userID string) (int64, error) {
	if m.users != nil {
		return m.users.GetStorageBytes(userID)
	}
	return m.fallbackStorage[userID], nil
}

func (m *MemoryStore) reserveStorage(userID string, delta, quotaBytes int64) error {
	if m.users != nil {
		return m.users.ReserveStorage(userID, delta, quotaBytes)
	}
	used := m.fallbackStorage[userID]
	if used+delta > quotaBytes {
		return apperr.Quota(used)
	}
	m.fallbackStorage[userID] = used + delta
	return nil
}

func (m *MemoryStore) releaseStorage(userID string, delta int64) error {
	if m.users != nil {
		return m.users.ReleaseStorage(userID, delta)
	}
	used := m.fallbackStorage[userID] - delta
	if used < 0 {
		used = 0
	}
	m.fallbackStorage[userID] = used
	return nil
}

func (m *MemoryStore) toDoc(r docRecord, detail bool) Doc {
	doc := Doc{
		ID:            r.ID,
		KBID:          r.KBID,
		Title:         r.Title,
		Status:        identity.DocStatusLabel(r.Status),
		Year:          r.Year,
		Authors:       r.Authors,
		FailureReason: r.FailureReason,
		RelativePath:  r.RelativePath,
		CreatedAt:     clock.Format(r.CreatedAt),
	}
	if doc.Authors == nil {
		doc.Authors = []string{}
	}
	code := ""
	if r.GraphStatus != nil {
		code = *r.GraphStatus
	}
	doc.GraphStatus = identity.GraphStatusLabel(code)

	tagSet := m.docTags[r.ID]
	tags := make([]Tag, 0, len(tagSet))
	for tagID := range tagSet {
		if t, ok := m.tags[tagID]; ok {
			tags = append(tags, Tag{ID: t.ID, Name: t.Name})
		}
	}
	sort.Slice(tags, func(i, j int) bool {
		return tags[i].Name < tags[j].Name
	})
	doc.Tags = tags

	if r.SkippedPages == nil {
		doc.SkippedPages = []int{}
	} else {
		doc.SkippedPages = r.SkippedPages
	}

	if detail {
		doc.Venue = r.Venue
		doc.Volume = r.Volume
		doc.Issue = r.Issue
		doc.Pages = r.Pages
		doc.DOI = r.DOI
		doc.PageCount = r.PageCount
		doc.HasPhysicalPages = r.HasPhysicalPages
		doc.ManualRetryCount = r.ManualRetryCount
		doc.SizeBytes = &r.SizeBytes
	}
	return doc
}

func (m *MemoryStore) ListKBs(ctx context.Context, owner string) ([]KB, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]KB, 0)
	records := make([]kbRecord, 0)
	for _, kb := range m.kbs {
		if kb.OwnerID == owner {
			records = append(records, kb)
		}
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].CreatedAt.Equal(records[j].CreatedAt) {
			return records[i].ID > records[j].ID
		}
		return records[i].CreatedAt.After(records[j].CreatedAt)
	})

	for _, r := range records {
		docCount := 0
		readyCount := 0
		for _, d := range m.docs {
			if d.KBID == r.ID {
				docCount++
				if d.Status == "ready" {
					readyCount++
				}
			}
		}
		out = append(out, KB{
			ID:                 r.ID,
			Name:               r.Name,
			GraphEnabled:       r.GraphEnabled,
			AnswerInstruction:  r.AnswerInstruction,
			RetrievalThreshold: r.RetrievalThreshold,
			DocumentCount:      docCount,
			ReadyCount:         readyCount,
		})
	}
	return out, nil
}

func (m *MemoryStore) CreateKB(ctx context.Context, owner, name string) (KB, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	name = strings.TrimSpace(name)
	if !NameOK(name) {
		return KB{}, apperr.WithDetails("VALIDATION_ERROR", "名称需要 1 到 50 个字符", map[string]any{"field": "name"})
	}

	id := uuid.NewString()
	now := m.clock.Now().UTC()
	r := kbRecord{
		ID:           id,
		OwnerID:      owner,
		Name:         name,
		GraphEnabled: true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	m.kbs[id] = r
	return KB{
		ID:           id,
		Name:         name,
		GraphEnabled: true,
	}, nil
}

func (m *MemoryStore) GetKB(ctx context.Context, owner, id string) (KB, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	r, ok := m.kbs[id]
	if !ok || r.OwnerID != owner {
		return KB{}, apperr.ErrNotFound
	}
	docCount := 0
	readyCount := 0
	for _, d := range m.docs {
		if d.KBID == r.ID {
			docCount++
			if d.Status == "ready" {
				readyCount++
			}
		}
	}
	return KB{
		ID:                 r.ID,
		Name:               r.Name,
		GraphEnabled:       r.GraphEnabled,
		AnswerInstruction:  r.AnswerInstruction,
		RetrievalThreshold: r.RetrievalThreshold,
		DocumentCount:      docCount,
		ReadyCount:         readyCount,
	}, nil
}

func (m *MemoryStore) Upload(ctx context.Context, owner, kbID string, meta UploadMeta, data []byte) (UploadResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	kb, ok := m.kbs[kbID]
	kbOwned := ok && kb.OwnerID == owner
	if !kbOwned {
		return UploadResult{}, apperr.ErrNotFound
	}

	if int64(len(data)) > apperr.MaxUploadBytes {
		return UploadResult{}, apperr.ErrFileTooLarge
	}

	info := pdfcheck.Inspect(data)
	ext := strings.ToLower(meta.FileName)
	isPDF := info.IsPDF && strings.HasSuffix(ext, ".pdf")
	if !isPDF {
		return UploadResult{}, apperr.ErrUnsupported
	}

	title := strings.TrimSpace(meta.Title)
	if title == "" || len([]rune(title)) > 500 {
		return UploadResult{}, apperr.WithDetails("VALIDATION_ERROR", "题名不能为空，且不超过 500 个字符", map[string]any{"field": "title"})
	}

	if meta.Authors == nil {
		meta.Authors = []string{}
	}

	sum := cryptobox.SHA256Hex(data)
	hash := cryptobox.SHA256Hex(append(append([]byte(sum), []byte(kbID+title+meta.Venue+cleanPath(meta.RelativePath))...), mustJSON(meta.Authors)...))

	if meta.IdempotencyKey != "" {
		idempKey := owner + ":" + meta.IdempotencyKey
		if rec, exists := m.idempotency[idempKey]; exists {
			if m.clock.Now().UTC().Sub(rec.CreatedAt) > 24*time.Hour {
				delete(m.idempotency, idempKey)
			} else if rec.RequestHash != hash {
				return UploadResult{}, apperr.ErrIdempotency
			} else {
				doc, exists := m.docs[rec.DocID]
				if !exists || doc.OwnerID != owner {
					delete(m.idempotency, idempKey)
				} else {
					return UploadResult{
						Status: rec.StatusCode,
						Doc:    m.toDoc(doc, true),
					}, nil
				}
			}
		}
	}

	storage, _ := m.getStorageBytes(owner)
	var existing *Existing
	if ownerSha, ok := m.shaIndex[owner]; ok {
		if existingDocID, found := ownerSha[sum]; found {
			if doc, docFound := m.docs[existingDocID]; docFound {
				existing = &Existing{
					ID:     doc.ID,
					KBID:   doc.KBID,
					Status: doc.Status,
				}
			}
		}
	}

	outcome := DecideUpload(UploadInput{
		KBOwned:      kbOwned,
		Size:         int64(len(data)),
		IsPDF:        isPDF,
		Encrypted:    info.Encrypted,
		PageCount:    info.PageCount,
		StorageBytes: storage,
		Existing:     existing,
	})
	if outcome.Err != nil {
		return UploadResult{}, outcome.Err
	}

	now := m.clock.Now().UTC()

	if outcome.Reuse {
		doc := m.docs[existing.ID]
		key := doc.StorageKey
		if key == "" {
			key = owner + "/" + doc.ID
		}
		if err := m.blobs.Put(key, data); err != nil {
			return UploadResult{}, err
		}

		doc.Status = "pending"
		doc.FailureReason = nil
		doc.KBID = kbID
		doc.Title = title
		doc.Authors = meta.Authors
		doc.Year = meta.Year
		doc.Venue = cleanStrPtr(meta.Venue)
		doc.RelativePath = cleanStrPtr(cleanPath(meta.RelativePath))
		doc.StorageKey = key
		doc.SizeBytes = int64(len(data))
		doc.CancelRequested = false
		doc.GraphStatus = nil
		doc.PageCount = info.PageCount
		doc.SkippedPages = []int{}
		doc.UpdatedAt = now

		m.docs[existing.ID] = doc

		if meta.IdempotencyKey != "" {
			m.idempotency[owner+":"+meta.IdempotencyKey] = idempotencyRecord{
				ID:          uuid.NewString(),
				UserID:      owner,
				Key:         meta.IdempotencyKey,
				RequestHash: hash,
				DocID:       doc.ID,
				StatusCode:  200,
				CreatedAt:   now,
			}
		}

		return UploadResult{
			Status: 200,
			Doc:    m.toDoc(doc, true),
		}, nil
	}

	id := uuid.NewString()
	storageKey := owner + "/" + id
	if err := m.reserveStorage(owner, int64(len(data)), apperr.QuotaBytes); err != nil {
		return UploadResult{}, err
	}
	if err := m.blobs.Put(storageKey, data); err != nil {
		_ = m.releaseStorage(owner, int64(len(data)))
		return UploadResult{}, err
	}

	physical := true
	doc := docRecord{
		ID:               id,
		OwnerID:          owner,
		KBID:             kbID,
		SourceType:       "pdf",
		Title:            title,
		Authors:          meta.Authors,
		Year:             meta.Year,
		Venue:            cleanStrPtr(meta.Venue),
		RelativePath:     cleanStrPtr(cleanPath(meta.RelativePath)),
		Status:           "pending",
		SHA256:           sum,
		SizeBytes:        int64(len(data)),
		StorageKey:       storageKey,
		PageCount:        info.PageCount,
		HasPhysicalPages: &physical,
		SkippedPages:     []int{},
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	m.docs[id] = doc
	if m.shaIndex[owner] == nil {
		m.shaIndex[owner] = make(map[string]string)
	}
	m.shaIndex[owner][sum] = id

	if meta.IdempotencyKey != "" {
		m.idempotency[owner+":"+meta.IdempotencyKey] = idempotencyRecord{
			ID:          uuid.NewString(),
			UserID:      owner,
			Key:         meta.IdempotencyKey,
			RequestHash: hash,
			DocID:       id,
			StatusCode:  201,
			CreatedAt:   now,
		}
	}

	return UploadResult{
		Status: 201,
		Doc:    m.toDoc(doc, true),
	}, nil
}

func (m *MemoryStore) ListDocs(ctx context.Context, owner, kbID, q string, tagIDs []string, page, size int) ([]Doc, int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}

	trimmedQ := strings.ToLower(strings.TrimSpace(q))

	tagFilter := make(map[string]struct{})
	for _, id := range tagIDs {
		if _, err := uuid.Parse(id); err == nil {
			tagFilter[id] = struct{}{}
		}
	}
	hasTagFilter := len(tagIDs) > 0

	matched := make([]docRecord, 0)
	for _, doc := range m.docs {
		if doc.OwnerID != owner {
			continue
		}
		if kbID != "" && doc.KBID != kbID {
			continue
		}
		if trimmedQ != "" && !strings.Contains(strings.ToLower(doc.Title), trimmedQ) {
			continue
		}
		if hasTagFilter {
			docTagSet := m.docTags[doc.ID]
			found := false
			for tagID := range docTagSet {
				if _, ok := tagFilter[tagID]; ok {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		matched = append(matched, doc)
	}

	sort.Slice(matched, func(i, j int) bool {
		if matched[i].CreatedAt.Equal(matched[j].CreatedAt) {
			return matched[i].ID > matched[j].ID
		}
		return matched[i].CreatedAt.After(matched[j].CreatedAt)
	})

	total := len(matched)
	start := (page - 1) * size
	if start >= total {
		return []Doc{}, total, nil
	}
	end := start + size
	if end > total {
		end = total
	}

	paged := matched[start:end]
	out := make([]Doc, 0, len(paged))
	for _, r := range paged {
		out = append(out, m.toDoc(r, false))
	}
	return out, total, nil
}

func (m *MemoryStore) GetDoc(ctx context.Context, owner, id string) (Doc, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	doc, ok := m.docs[id]
	if !ok || doc.OwnerID != owner {
		return Doc{}, apperr.ErrNotFound
	}
	return m.toDoc(doc, true), nil
}

func (m *MemoryStore) PatchDoc(ctx context.Context, owner, id string, raw map[string]json.RawMessage) (Doc, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	doc, ok := m.docs[id]
	if !ok || doc.OwnerID != owner {
		return Doc{}, apperr.ErrNotFound
	}

	if v, ok := raw["title"]; ok {
		var title string
		if json.Unmarshal(v, &title) != nil || strings.TrimSpace(title) == "" || len([]rune(strings.TrimSpace(title))) > 500 {
			return Doc{}, apperr.WithDetails("VALIDATION_ERROR", "题名不能为空，且不超过 500 个字符", map[string]any{"field": "title"})
		}
		doc.Title = strings.TrimSpace(title)
	}

	if v, ok := raw["authors"]; ok {
		var authors []string
		if json.Unmarshal(v, &authors) != nil {
			return Doc{}, apperr.WithDetails("VALIDATION_ERROR", "作者需要是字符串数组", map[string]any{"field": "authors"})
		}
		doc.Authors = authors
	}

	for _, field := range []string{"venue", "volume", "issue", "pages", "doi"} {
		v, ok := raw[field]
		if !ok {
			continue
		}
		if string(v) == "null" {
			switch field {
			case "venue":
				doc.Venue = nil
			case "volume":
				doc.Volume = nil
			case "issue":
				doc.Issue = nil
			case "pages":
				doc.Pages = nil
			case "doi":
				doc.DOI = nil
			}
			continue
		}
		var text string
		if json.Unmarshal(v, &text) != nil {
			return Doc{}, apperr.WithDetails("VALIDATION_ERROR", "字段格式不正确", map[string]any{"field": field})
		}
		ptr := &text
		switch field {
		case "venue":
			doc.Venue = ptr
		case "volume":
			doc.Volume = ptr
		case "issue":
			doc.Issue = ptr
		case "pages":
			doc.Pages = ptr
		case "doi":
			doc.DOI = ptr
		}
	}

	if v, ok := raw["year"]; ok {
		if string(v) == "null" {
			doc.Year = nil
		} else {
			var year int
			if json.Unmarshal(v, &year) != nil || year < 0 || year > 3000 {
				return Doc{}, apperr.WithDetails("VALIDATION_ERROR", "年份不正确", map[string]any{"field": "year"})
			}
			doc.Year = &year
		}
	}

	doc.UpdatedAt = m.clock.Now().UTC()
	m.docs[id] = doc
	return m.toDoc(doc, true), nil
}

func (m *MemoryStore) Delete(ctx context.Context, owner, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	doc, ok := m.docs[id]
	if !ok || doc.OwnerID != owner {
		return apperr.ErrNotFound
	}

	doc.CancelRequested = true
	_ = m.releaseStorage(owner, doc.SizeBytes)
	if doc.StorageKey != "" {
		_ = m.blobs.Delete(doc.StorageKey)
	}

	if ownerSha, ok := m.shaIndex[owner]; ok {
		delete(ownerSha, doc.SHA256)
	}
	delete(m.docs, id)

	if tags, ok := m.docTags[id]; ok {
		for tagID := range tags {
			if docs, exists := m.tagDocs[tagID]; exists {
				delete(docs, id)
			}
		}
		delete(m.docTags, id)
	}
	m.deleteDocChunksLocked(id)

	return nil
}

func (m *MemoryStore) File(ctx context.Context, owner, id string) (string, []byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	doc, ok := m.docs[id]
	if !ok || doc.OwnerID != owner || doc.StorageKey == "" {
		return "", nil, apperr.ErrNotFound
	}
	data, err := m.blobs.Get(doc.StorageKey)
	if err != nil {
		return "", nil, apperr.ErrNotFound
	}
	return doc.Title, data, nil
}

func (m *MemoryStore) ListTags(ctx context.Context, owner string) ([]Tag, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]Tag, 0)
	records := make([]tagRecord, 0)
	for _, t := range m.tags {
		if t.OwnerID == owner {
			records = append(records, t)
		}
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].CreatedAt.Before(records[j].CreatedAt)
	})
	for _, r := range records {
		out = append(out, Tag{ID: r.ID, Name: r.Name})
	}
	return out, nil
}

func (m *MemoryStore) CreateTag(ctx context.Context, owner, name string) (Tag, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	name = strings.TrimSpace(name)
	if !NameOK(name) {
		return Tag{}, apperr.WithDetails("VALIDATION_ERROR", "名称需要 1 到 50 个字符", map[string]any{"field": "name"})
	}

	if ownerNames, ok := m.tagNameIndex[owner]; ok {
		if _, exists := ownerNames[name]; exists {
			return Tag{}, apperr.ErrTagExists
		}
	} else {
		m.tagNameIndex[owner] = make(map[string]string)
	}

	id := uuid.NewString()
	t := tagRecord{
		ID:        id,
		OwnerID:   owner,
		Name:      name,
		CreatedAt: m.clock.Now().UTC(),
	}
	m.tags[id] = t
	m.tagNameIndex[owner][name] = id
	return Tag{ID: id, Name: name}, nil
}

func (m *MemoryStore) PatchTag(ctx context.Context, owner, id, name string) (Tag, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	tag, ok := m.tags[id]
	if !ok || tag.OwnerID != owner {
		return Tag{}, apperr.ErrNotFound
	}

	name = strings.TrimSpace(name)
	if !NameOK(name) {
		return Tag{}, apperr.WithDetails("VALIDATION_ERROR", "名称需要 1 到 50 个字符", map[string]any{"field": "name"})
	}

	if ownerNames, ok := m.tagNameIndex[owner]; ok {
		if existingID, exists := ownerNames[name]; exists && existingID != id {
			return Tag{}, apperr.ErrTagExists
		}
		delete(ownerNames, tag.Name)
		ownerNames[name] = id
	}

	tag.Name = name
	m.tags[id] = tag
	return Tag{ID: id, Name: name}, nil
}

func (m *MemoryStore) DeleteTag(ctx context.Context, owner, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	tag, ok := m.tags[id]
	if !ok || tag.OwnerID != owner {
		return apperr.ErrNotFound
	}

	delete(m.tags, id)
	if ownerNames, ok := m.tagNameIndex[owner]; ok {
		delete(ownerNames, tag.Name)
	}

	if docSet, ok := m.tagDocs[id]; ok {
		for docID := range docSet {
			if tags, exists := m.docTags[docID]; exists {
				delete(tags, id)
			}
		}
		delete(m.tagDocs, id)
	}

	return nil
}

func (m *MemoryStore) PutTags(ctx context.Context, owner, docID string, tagIDs []string) (Doc, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	doc, ok := m.docs[docID]
	if !ok || doc.OwnerID != owner {
		return Doc{}, apperr.ErrNotFound
	}

	dedup := make(map[string]struct{})
	for _, id := range tagIDs {
		if _, err := uuid.Parse(id); err != nil {
			return Doc{}, apperr.ErrNotFound
		}
		tag, exists := m.tags[id]
		if !exists || tag.OwnerID != owner {
			return Doc{}, apperr.ErrNotFound
		}
		dedup[id] = struct{}{}
	}

	if oldTags, ok := m.docTags[docID]; ok {
		for tagID := range oldTags {
			if docSet, exists := m.tagDocs[tagID]; exists {
				delete(docSet, docID)
			}
		}
	}

	m.docTags[docID] = dedup
	for tagID := range dedup {
		if m.tagDocs[tagID] == nil {
			m.tagDocs[tagID] = make(map[string]struct{})
		}
		m.tagDocs[tagID][docID] = struct{}{}
	}

	return m.toDoc(doc, true), nil
}

func (m *MemoryStore) SetDocStatusForTest(owner, docID, status string, failureReason *string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	doc, ok := m.docs[docID]
	if !ok || doc.OwnerID != owner {
		return apperr.ErrNotFound
	}
	doc.Status = status
	doc.FailureReason = failureReason
	doc.UpdatedAt = m.clock.Now().UTC()
	m.docs[docID] = doc
	return nil
}

// MemoryReadingDoc 是供内存按页阅读模块读取的只读资料元信息。
type MemoryReadingDoc struct {
	ID               string
	OwnerID          string
	Title            string
	SourceType       string
	Status           string
	PageCount        int
	HasPhysicalPages bool
	HasOriginalFile  bool
}

// MemoryReadingChunk 是供内存按页阅读模块读取的单页切块片段。
type MemoryReadingChunk struct {
	Text string
}

// ReadingDocMeta 返回属于指定所有者的文档阅读元信息。
func (m *MemoryStore) ReadingDocMeta(owner, docID string) (MemoryReadingDoc, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	doc, ok := m.docs[docID]
	if !ok || doc.OwnerID != owner {
		return MemoryReadingDoc{}, false
	}
	pageCount := 0
	if doc.PageCount != nil {
		pageCount = *doc.PageCount
	}
	hasPhysical := true
	if doc.HasPhysicalPages != nil {
		hasPhysical = *doc.HasPhysicalPages
	}
	return MemoryReadingDoc{
		ID:               doc.ID,
		OwnerID:          doc.OwnerID,
		Title:            doc.Title,
		SourceType:       doc.SourceType,
		Status:           doc.Status,
		PageCount:        pageCount,
		HasPhysicalPages: hasPhysical,
		HasOriginalFile:  strings.EqualFold(doc.SourceType, "pdf") && doc.StorageKey != "",
	}, true
}

// ReadingPageChunks 按段落顺序返回文档指定物理页下的只读切块文本。
func (m *MemoryStore) ReadingPageChunks(owner, docID string, page int) ([]MemoryReadingChunk, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	doc, ok := m.docs[docID]
	if !ok || doc.OwnerID != owner {
		return nil, apperr.ErrNotFound
	}
	type item struct {
		para int
		id   string
		text string
	}
	var matched []item
	for _, cid := range m.docChunks[docID] {
		ch, exists := m.chunks[cid]
		if !exists || ch.PageStart != page {
			continue
		}
		matched = append(matched, item{para: ch.ParagraphIndex, id: ch.ID, text: ch.Text})
	}
	sort.Slice(matched, func(i, j int) bool {
		if matched[i].para != matched[j].para {
			return matched[i].para < matched[j].para
		}
		return matched[i].id < matched[j].id
	})
	out := make([]MemoryReadingChunk, len(matched))
	for i, it := range matched {
		out[i] = MemoryReadingChunk{Text: it.text}
	}
	return out, nil
}

func cleanStrPtr(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}
