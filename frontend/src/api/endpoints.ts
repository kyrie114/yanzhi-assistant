import {
  downloadFileBlob,
  openSSE,
  openSSEOrJSON,
  request,
  type ReadingTranslationResponse,
  type SSEConnection,
} from './http';
import {
  parseChunkDetail,
  parseDoc,
  parseDocPage,
  parseGraphEdgeDetail,
  parseGraphExtraction,
  parseGraphNodeDetail,
  parseGraphView,
  parseSuggestedQuestions,
  parseHealthStatus,
  parseKB,
  parseKBList,
  parseLoginResult,
  parseModelConfig,
  parseModelConfigList,
  parseProbeResult,
  parsePublicUser,
  parseQASession,
  parseQASessionPage,
  parseReadingPage,
  parseTag,
  parseTagList,
  parseUnresolvedReferences,
  serializeScope,
  type ChunkDetail,
  type Doc,
  type GraphEdgeDetail,
  type GraphExtraction,
  type GraphNodeDetail,
  type GraphScope,
  type GraphView,
  type SuggestedQuestions,
  type HealthStatus,
  type KB,
  type LoginResult,
  type ModelConfig,
  type ModelKind,
  type PageResult,
  type ProbeResult,
  type PublicUser,
  type QAScope,
  type QASession,
  type ReadingPage,
  type Tag,
  type UnresolvedReferences,
} from './types';

function noop(): void {}

export function getHealth(): Promise<HealthStatus> {
  return request('/api/v1/health', parseHealthStatus, { skipAuth: true, skipAuthRedirect: true });
}

export function register(input: { email: string; name: string; password: string }): Promise<PublicUser> {
  return request('/api/v1/auth/register', parsePublicUser, {
    method: 'POST',
    body: input,
    skipAuth: true,
    skipAuthRedirect: true,
  });
}

export function login(input: { email: string; password: string }): Promise<LoginResult> {
  return request('/api/v1/auth/login', parseLoginResult, {
    method: 'POST',
    body: input,
    skipAuth: true,
    skipAuthRedirect: true,
  });
}

export function logout(): Promise<void> {
  return request('/api/v1/auth/logout', noop, {
    method: 'POST',
    skipAuthRedirect: true,
  });
}

export function changePassword(input: { oldPassword: string; newPassword: string }): Promise<void> {
  return request('/api/v1/auth/password', noop, {
    method: 'POST',
    body: {
      old_password: input.oldPassword,
      new_password: input.newPassword,
    },
  });
}

export function getMe(): Promise<PublicUser> {
  return request('/api/v1/me', parsePublicUser);
}

export function listKBs(): Promise<KB[]> {
  return request('/api/v1/kbs', parseKBList);
}

export function createKB(name: string): Promise<KB> {
  return request('/api/v1/kbs', parseKB, {
    method: 'POST',
    body: { name },
  });
}

export function getKB(kbId: string): Promise<KB> {
  return request(`/api/v1/kbs/${encodeURIComponent(kbId)}`, parseKB);
}

export function patchKBSettings(
  kbId: string,
  input: {
    answerInstruction: string | null;
    retrievalThreshold: number | null;
    graphEnabled: boolean;
  },
): Promise<KB> {
  return request(`/api/v1/kbs/${encodeURIComponent(kbId)}/settings`, parseKB, {
    method: 'PATCH',
    body: {
      answer_instruction: input.answerInstruction,
      retrieval_threshold: input.retrievalThreshold,
      graph_enabled: input.graphEnabled,
    },
  });
}

export function uploadDocument(
  kbId: string,
  input: {
    file: File;
    title: string;
    authors: string[];
    year: number | null;
    venue: string;
  },
): Promise<Doc> {
  const form = new FormData();
  form.append('file', input.file);
  form.append('title', input.title);
  if (input.authors.length > 0) {
    form.append('authors', JSON.stringify(input.authors));
  }
  if (input.year !== null) {
    form.append('year', String(input.year));
  }
  if (input.venue.trim()) {
    form.append('venue', input.venue.trim());
  }
  return request(`/api/v1/kbs/${encodeURIComponent(kbId)}/documents`, parseDoc, {
    method: 'POST',
    body: form,
  });
}

export function listDocuments(params: {
  q?: string;
  kbId?: string;
  tagIds?: string[];
  page?: number;
  pageSize?: number;
}): Promise<PageResult<Doc>> {
  const query = new URLSearchParams();
  if (params.q && params.q.trim()) {
    query.set('q', params.q.trim());
  }
  if (params.kbId) {
    query.set('kb_id', params.kbId);
  }
  for (const tagId of params.tagIds ?? []) {
    query.append('tag_id', tagId);
  }
  if (params.page && params.page > 0) {
    query.set('page', String(params.page));
  }
  if (params.pageSize && params.pageSize > 0) {
    query.set('page_size', String(params.pageSize));
  }
  const qs = query.toString();
  return request(qs ? `/api/v1/documents?${qs}` : '/api/v1/documents', parseDocPage);
}

export function getDocument(docId: string): Promise<Doc> {
  return request(`/api/v1/documents/${encodeURIComponent(docId)}`, parseDoc);
}

export function patchDocument(
  docId: string,
  input: {
    title: string;
    authors: string[];
    year: number | null;
    venue: string | null;
    volume: string | null;
    issue: string | null;
    pages: string | null;
    doi: string | null;
  },
): Promise<Doc> {
  return request(`/api/v1/documents/${encodeURIComponent(docId)}`, parseDoc, {
    method: 'PATCH',
    body: input,
  });
}

export function deleteDocument(docId: string): Promise<void> {
  return request(`/api/v1/documents/${encodeURIComponent(docId)}`, noop, {
    method: 'DELETE',
  });
}

export function retryDocumentIngest(docId: string): Promise<void> {
  return request(`/api/v1/documents/${encodeURIComponent(docId)}/retries`, noop, {
    method: 'POST',
  });
}

export function downloadDocumentFile(docId: string): Promise<{ blob: Blob; filename: string }> {
  return downloadFileBlob(`/api/v1/documents/${encodeURIComponent(docId)}/file`);
}

export function getDocumentPage(docId: string, page: number): Promise<ReadingPage> {
  return request(
    `/api/v1/documents/${encodeURIComponent(docId)}/pages/${encodeURIComponent(String(page))}`,
    parseReadingPage,
  );
}

export function startPageTranslation(
  docId: string,
  page: number,
  signal?: AbortSignal,
): Promise<ReadingTranslationResponse> {
  return openSSEOrJSON(
    `/api/v1/documents/${encodeURIComponent(docId)}/pages/${encodeURIComponent(String(page))}/translation`,
    { method: 'POST', signal },
  );
}

export function getSuggestedQuestions(
  scope: { kind: 'kb' | 'document'; id: string },
  asked = '',
): Promise<SuggestedQuestions> {
  const path =
    scope.kind === 'kb'
      ? `/api/v1/kbs/${encodeURIComponent(scope.id)}/suggested-questions`
      : `/api/v1/documents/${encodeURIComponent(scope.id)}/suggested-questions`;
  const query = asked.trim() ? `?asked=${encodeURIComponent(asked.trim())}` : '';
  return request(path + query, parseSuggestedQuestions);
}

export function getChunkDetail(docId: string, chunkId: string): Promise<ChunkDetail> {
  return request(
    `/api/v1/documents/${encodeURIComponent(docId)}/chunks/${encodeURIComponent(chunkId)}`,
    parseChunkDetail,
  );
}

export function getGraph(scope: GraphScope): Promise<GraphView> {
  const query = new URLSearchParams();
  if (scope.kbId) {
    query.set('kb_id', scope.kbId);
  }
  for (const tagId of scope.tagIds ?? []) {
    query.append('tag_id', tagId);
  }
  const qs = query.toString();
  return request(qs ? `/api/v1/graph?${qs}` : '/api/v1/graph', parseGraphView);
}

export function getDocumentGraph(documentId: string): Promise<GraphView> {
  return request(`/api/v1/documents/${encodeURIComponent(documentId)}/graph`, parseGraphView);
}

export function getGraphNode(id: string): Promise<GraphNodeDetail> {
  return request(`/api/v1/graph/nodes/${encodeURIComponent(id)}`, parseGraphNodeDetail);
}

export function getGraphEdge(id: string): Promise<GraphEdgeDetail> {
  return request(`/api/v1/graph/edges/${encodeURIComponent(id)}`, parseGraphEdgeDetail);
}

export function getGraphExtraction(documentId: string): Promise<GraphExtraction> {
  return request(
    `/api/v1/documents/${encodeURIComponent(documentId)}/graph-extraction`,
    parseGraphExtraction,
  );
}

export function retryGraphExtraction(documentId: string): Promise<void> {
  return request(
    `/api/v1/documents/${encodeURIComponent(documentId)}/graph-extraction/retries`,
    noop,
    { method: 'POST' },
  );
}

export function getUnresolvedReferences(documentId: string): Promise<UnresolvedReferences> {
  return request(
    `/api/v1/documents/${encodeURIComponent(documentId)}/unresolved-references`,
    parseUnresolvedReferences,
  );
}

export function listTags(): Promise<Tag[]> {
  return request('/api/v1/tags', parseTagList);
}

export function createTag(name: string): Promise<Tag> {
  return request('/api/v1/tags', parseTag, {
    method: 'POST',
    body: { name },
  });
}

export function patchTag(tagId: string, name: string): Promise<Tag> {
  return request(`/api/v1/tags/${encodeURIComponent(tagId)}`, parseTag, {
    method: 'PATCH',
    body: { name },
  });
}

export function deleteTag(tagId: string): Promise<void> {
  return request(`/api/v1/tags/${encodeURIComponent(tagId)}`, noop, {
    method: 'DELETE',
  });
}

export function putDocumentTags(docId: string, tagIds: string[]): Promise<Doc> {
  return request(`/api/v1/documents/${encodeURIComponent(docId)}/tags`, parseDoc, {
    method: 'PUT',
    body: { tag_ids: tagIds },
  });
}

export function createQASession(title = ''): Promise<QASession> {
  return request('/api/v1/qa/sessions', parseQASession, {
    method: 'POST',
    body: { title },
  });
}

export function listQASessions(page = 1, pageSize = 50): Promise<PageResult<QASession>> {
  const qs = new URLSearchParams({
    page: String(page),
    page_size: String(pageSize),
  }).toString();
  return request(`/api/v1/qa/sessions?${qs}`, parseQASessionPage);
}

export function getQASession(sessionId: string): Promise<QASession> {
  return request(`/api/v1/qa/sessions/${encodeURIComponent(sessionId)}`, parseQASession);
}

export function patchQASession(sessionId: string, title: string): Promise<QASession> {
  return request(`/api/v1/qa/sessions/${encodeURIComponent(sessionId)}`, parseQASession, {
    method: 'PATCH',
    body: { title },
  });
}

export function deleteQASession(sessionId: string): Promise<void> {
  return request(`/api/v1/qa/sessions/${encodeURIComponent(sessionId)}`, noop, {
    method: 'DELETE',
  });
}

export function startQATurnStream(
  sessionId: string,
  input: { question: string; scope: QAScope },
  signal?: AbortSignal,
): Promise<SSEConnection> {
  return openSSE(`/api/v1/qa/sessions/${encodeURIComponent(sessionId)}/turns`, {
    method: 'POST',
    body: {
      question: input.question,
      scope: serializeScope(input.scope),
      web_search: false,
    },
    signal,
  });
}

export function resumeQATurnStream(
  sessionId: string,
  turnId: string,
  afterSeq: number | null,
  signal?: AbortSignal,
): Promise<SSEConnection> {
  const qs = afterSeq !== null ? `?after=${afterSeq}` : '';
  return openSSE(
    `/api/v1/qa/sessions/${encodeURIComponent(sessionId)}/turns/${encodeURIComponent(turnId)}/stream${qs}`,
    { method: 'GET', signal },
  );
}

export function stopQASession(sessionId: string): Promise<void> {
  return request(`/api/v1/qa/sessions/${encodeURIComponent(sessionId)}/stop`, noop, {
    method: 'POST',
  });
}

export function listModelConfigs(): Promise<ModelConfig[]> {
  return request('/api/v1/admin/model-configs', parseModelConfigList);
}

export function saveModelConfig(
  kind: ModelKind,
  input: {
    baseUrl: string;
    modelName: string;
    apiKey?: string;
    dimension?: number | null;
    confirmReindex?: boolean;
  },
): Promise<ModelConfig> {
  const body: Record<string, unknown> = {
    base_url: input.baseUrl,
    model_name: input.modelName,
    confirm_reindex: Boolean(input.confirmReindex),
  };
  if (input.apiKey !== undefined && input.apiKey !== '') {
    body.api_key = input.apiKey;
  }
  if (input.dimension !== undefined && input.dimension !== null) {
    body.dimension = input.dimension;
  }
  return request(`/api/v1/admin/model-configs/${kind}`, parseModelConfig, {
    method: 'PUT',
    body,
  });
}

export function testModelConnection(
  kind: ModelKind,
  input: {
    baseUrl: string;
    modelName: string;
    apiKey: string;
    dimension?: number | null;
  },
): Promise<ProbeResult> {
  return request(`/api/v1/admin/model-configs/${kind}/connection-tests`, parseProbeResult, {
    method: 'POST',
    body: {
      base_url: input.baseUrl,
      model_name: input.modelName,
      api_key: input.apiKey,
      dimension: input.dimension ?? 0,
    },
  });
}
