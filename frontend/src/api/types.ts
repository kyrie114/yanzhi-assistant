import type { DocStatus } from '../lib/status';

export type UserRole = '注册用户' | '系统管理员';
export type GraphStatus = '待抽取' | '抽取中' | '已完成' | '失败';
export type AnswerKind = 'kb' | 'smalltalk' | 'refused';
export type ModelKind = 'chat' | 'embedding' | 'rerank' | 'vision';
type ProbeStatus = 'ok' | 'unauthorized' | 'unreachable';

export interface LoginResult {
  accessToken: string;
  tokenType: string;
  expiresIn: number;
  role: UserRole;
  emailVerified: boolean;
}

export interface PublicUser {
  id: string;
  email: string;
  name: string;
  role: UserRole;
  emailVerified: boolean;
  storageBytes: number;
}

export interface HealthStatus {
  status: string;
  docreader: string;
}

export interface Tag {
  id: string;
  name: string;
}

export interface KB {
  id: string;
  name: string;
  graphEnabled: boolean;
  answerInstruction: string | null;
  retrievalThreshold: number | null;
  documentCount: number;
  readyCount: number;
}

export interface Doc {
  id: string;
  kbId: string;
  title: string;
  status: DocStatus;
  graphStatus: GraphStatus | null;
  year: number | null;
  authors: string[];
  tags: Tag[];
  failureReason: string | null;
  skippedPages: number[];
  relativePath: string | null;
  venue: string | null;
  volume: string | null;
  issue: string | null;
  pages: string | null;
  doi: string | null;
  pageCount: number | null;
  hasPhysicalPages: boolean | null;
  manualRetryCount: number | null;
  sizeBytes: number | null;
  createdAt: string;
}

export interface PageResult<T> {
  items: T[];
  page: number;
  pageSize: number;
  total: number;
}

export interface ChunkDetail {
  text: string;
  pageStart: number;
  parentText: string;
}

export interface GraphScope {
  kbId?: string;
  tagIds?: string[];
}

export interface GraphNode {
  id: string;
  name: string;
  type: string;
  documentId: string | null;
}

export interface GraphEdge {
  id: string;
  type: string;
  source: string;
  target: string;
  suspended: boolean;
}

export interface GraphView {
  nodes: GraphNode[];
  edges: GraphEdge[];
  truncated: boolean;
}

export interface GraphSourcePage {
  documentId: string;
  title: string;
  pageStart: number;
}

export interface GraphNodeDetail {
  id: string;
  name: string;
  type: string;
  documentId: string | null;
  sources: GraphSourcePage[];
}

export interface GraphEvidence {
  documentId: string;
  chunkId: string;
  pageStart: number;
  sentence: string;
}

export interface GraphEdgeDetail {
  id: string;
  type: string;
  source: string;
  target: string;
  evidence: GraphEvidence[];
}

export interface GraphStage {
  name: string;
  status: string;
}

export interface GraphExtraction {
  graphStatus: GraphStatus | null;
  failureReason: string | null;
  manualRetryCount: number;
  stages: GraphStage[];
}

export type UnresolvedReferences = string[];

export type QAScope =
  | { type: 'all' }
  | { type: 'kb'; kbId: string }
  | { type: 'tags'; tagIds: string[] }
  | { type: 'documents'; documentIds: string[] };

export interface Citation {
  chunkId: string | null;
  documentId: string | null;
  label: string;
  origin: string;
  graphSupplement: boolean;
}

interface QATurn {
  id: string;
  question: string;
  answer: string;
  answerKind: AnswerKind;
  rewritten: boolean;
  reranked: boolean;
  createdAt: string;
  citations: Citation[];
}

export interface QASession {
  id: string;
  title: string;
  createdAt: string;
  updatedAt: string;
  turns: QATurn[];
}

export interface RetrievalItem {
  alias: string;
  chunkId: string;
  documentId: string;
  title: string;
  page: number;
  label: string;
  graphSupplement: boolean;
}

export interface ModelConfig {
  kind: ModelKind;
  baseUrl: string;
  modelName: string;
  apiKey: string;
  dimension: number | null;
}

export interface ProbeResult {
  result: ProbeStatus;
  message: string;
  elapsedMs: number;
}

export type UnknownRecord = Record<string, unknown>;

export function isRecord(val: unknown): val is UnknownRecord {
  return typeof val === 'object' && val !== null && !Array.isArray(val);
}

function expectRecord(val: unknown, label: string): UnknownRecord {
  if (!isRecord(val)) {
    throw new Error(`响应格式无效：${label}`);
  }
  return val;
}

function reqString(obj: UnknownRecord, key: string): string {
  const v = obj[key];
  if (typeof v !== 'string') {
    throw new Error(`响应缺少字符串字段：${key}`);
  }
  return v;
}

export function optString(obj: UnknownRecord, key: string, fallback = ''): string {
  const v = obj[key];
  return typeof v === 'string' ? v : fallback;
}

function optNullableString(obj: UnknownRecord, key: string): string | null {
  const v = obj[key];
  return typeof v === 'string' ? v : null;
}

function optNumber(obj: UnknownRecord, key: string, fallback = 0): number {
  const v = obj[key];
  return typeof v === 'number' && Number.isFinite(v) ? v : fallback;
}

function optNullableNumber(obj: UnknownRecord, key: string): number | null {
  const v = obj[key];
  return typeof v === 'number' && Number.isFinite(v) ? v : null;
}

export function optBoolean(obj: UnknownRecord, key: string, fallback = false): boolean {
  const v = obj[key];
  return typeof v === 'boolean' ? v : fallback;
}

function optNullableBoolean(obj: UnknownRecord, key: string): boolean | null {
  const v = obj[key];
  return typeof v === 'boolean' ? v : null;
}

function optStringArray(obj: UnknownRecord, key: string): string[] {
  const v = obj[key];
  if (!Array.isArray(v)) {
    return [];
  }
  return v.filter((item): item is string => typeof item === 'string');
}

function optNumberArray(obj: UnknownRecord, key: string): number[] {
  const v = obj[key];
  if (!Array.isArray(v)) {
    return [];
  }
  return v.filter((item): item is number => typeof item === 'number' && Number.isFinite(item));
}

export function isUserRole(val: unknown): val is UserRole {
  return val === '注册用户' || val === '系统管理员';
}

const DOC_STATUS_SET: ReadonlySet<string> = new Set(['待处理', '解析中', '向量化中', '已完成', '失败']);
function parseDocStatus(val: unknown): DocStatus {
  if (typeof val === 'string' && DOC_STATUS_SET.has(val)) {
    return val as DocStatus;
  }
  return '待处理';
}

const GRAPH_STATUS_SET: ReadonlySet<string> = new Set(['待抽取', '抽取中', '已完成', '失败']);
function parseGraphStatus(val: unknown): GraphStatus | null {
  if (typeof val === 'string' && GRAPH_STATUS_SET.has(val)) {
    return val as GraphStatus;
  }
  return null;
}

function parseAnswerKind(val: unknown): AnswerKind {
  if (val === 'kb' || val === 'smalltalk' || val === 'refused') {
    return val;
  }
  return 'kb';
}

function parseModelKind(val: unknown): ModelKind {
  if (val === 'chat' || val === 'embedding' || val === 'rerank' || val === 'vision') {
    return val;
  }
  throw new Error('未知模型类型');
}

function parseProbeStatus(val: unknown): ProbeStatus {
  if (val === 'ok' || val === 'unauthorized' || val === 'unreachable') {
    return val;
  }
  return 'unreachable';
}

export function parseLoginResult(raw: unknown): LoginResult {
  const obj = expectRecord(raw, 'LoginResult');
  const roleVal = obj.role;
  if (!isUserRole(roleVal)) {
    throw new Error('登录响应包含无效角色');
  }
  return {
    accessToken: reqString(obj, 'access_token'),
    tokenType: optString(obj, 'token_type', 'Bearer'),
    expiresIn: optNumber(obj, 'expires_in', 7200),
    role: roleVal,
    emailVerified: optBoolean(obj, 'email_verified', false),
  };
}

export function parsePublicUser(raw: unknown): PublicUser {
  const obj = expectRecord(raw, 'PublicUser');
  const roleVal = obj.role;
  if (!isUserRole(roleVal)) {
    throw new Error('用户信息包含无效角色');
  }
  return {
    id: reqString(obj, 'id'),
    email: reqString(obj, 'email'),
    name: reqString(obj, 'name'),
    role: roleVal,
    emailVerified: optBoolean(obj, 'email_verified', false),
    storageBytes: optNumber(obj, 'storage_bytes', 0),
  };
}

export function parseHealthStatus(raw: unknown): HealthStatus {
  const obj = expectRecord(raw, 'HealthStatus');
  return {
    status: optString(obj, 'status', 'unknown'),
    docreader: optString(obj, 'docreader', 'unknown'),
  };
}

export function parseTag(raw: unknown): Tag {
  const obj = expectRecord(raw, 'Tag');
  return {
    id: reqString(obj, 'id'),
    name: reqString(obj, 'name'),
  };
}

export function parseTagList(raw: unknown): Tag[] {
  if (!Array.isArray(raw)) {
    throw new Error('标签列表格式无效');
  }
  return raw.map(parseTag);
}

export function parseKB(raw: unknown): KB {
  const obj = expectRecord(raw, 'KB');
  return {
    id: reqString(obj, 'id'),
    name: reqString(obj, 'name'),
    graphEnabled: optBoolean(obj, 'graph_enabled', true),
    answerInstruction: optNullableString(obj, 'answer_instruction'),
    retrievalThreshold: optNullableNumber(obj, 'retrieval_threshold'),
    documentCount: optNumber(obj, 'document_count', 0),
    readyCount: optNumber(obj, 'ready_count', 0),
  };
}

export function parseKBList(raw: unknown): KB[] {
  const obj = expectRecord(raw, 'KBList');
  const items = Array.isArray(obj.items) ? obj.items : [];
  return items.map(parseKB);
}

export function parseDoc(raw: unknown): Doc {
  const obj = expectRecord(raw, 'Doc');
  const rawTags = Array.isArray(obj.tags) ? obj.tags : [];
  return {
    id: reqString(obj, 'id'),
    kbId: reqString(obj, 'kb_id'),
    title: reqString(obj, 'title'),
    status: parseDocStatus(obj.status),
    graphStatus: parseGraphStatus(obj.graph_status),
    year: optNullableNumber(obj, 'year'),
    authors: optStringArray(obj, 'authors'),
    tags: rawTags.map(parseTag),
    failureReason: optNullableString(obj, 'failure_reason'),
    skippedPages: optNumberArray(obj, 'skipped_pages'),
    relativePath: optNullableString(obj, 'relative_path'),
    venue: optNullableString(obj, 'venue'),
    volume: optNullableString(obj, 'volume'),
    issue: optNullableString(obj, 'issue'),
    pages: optNullableString(obj, 'pages'),
    doi: optNullableString(obj, 'doi'),
    pageCount: optNullableNumber(obj, 'page_count'),
    hasPhysicalPages: optNullableBoolean(obj, 'has_physical_pages'),
    manualRetryCount: optNullableNumber(obj, 'manual_retry_count'),
    sizeBytes: optNullableNumber(obj, 'size_bytes'),
    createdAt: optString(obj, 'created_at', ''),
  };
}

export function parseDocPage(raw: unknown): PageResult<Doc> {
  const obj = expectRecord(raw, 'DocPage');
  const items = Array.isArray(obj.items) ? obj.items.map(parseDoc) : [];
  return {
    items,
    page: optNumber(obj, 'page', 1),
    pageSize: optNumber(obj, 'page_size', 20),
    total: optNumber(obj, 'total', items.length),
  };
}

export function parseChunkDetail(raw: unknown): ChunkDetail {
  const obj = expectRecord(raw, 'ChunkDetail');
  return {
    text: reqString(obj, 'text'),
    pageStart: optNumber(obj, 'page_start', 1),
    parentText: optString(obj, 'parent_text', ''),
  };
}

function reqArray(obj: UnknownRecord, key: string): unknown[] {
  const v = obj[key];
  if (!Array.isArray(v)) {
    throw new Error(`响应缺少数组字段：${key}`);
  }
  return v;
}

function parseGraphNode(raw: unknown): GraphNode {
  const obj = expectRecord(raw, 'GraphNode');
  return {
    id: reqString(obj, 'id'),
    name: reqString(obj, 'name'),
    type: reqString(obj, 'type'),
    documentId: optNullableString(obj, 'document_id'),
  };
}

function parseGraphEdge(raw: unknown): GraphEdge {
  const obj = expectRecord(raw, 'GraphEdge');
  return {
    id: reqString(obj, 'id'),
    type: reqString(obj, 'type'),
    source: reqString(obj, 'source'),
    target: reqString(obj, 'target'),
    suspended: optBoolean(obj, 'suspended', false),
  };
}

export function parseGraphView(raw: unknown): GraphView {
  const obj = expectRecord(raw, 'GraphView');
  return {
    nodes: reqArray(obj, 'nodes').map(parseGraphNode),
    edges: reqArray(obj, 'edges').map(parseGraphEdge),
    truncated: optBoolean(obj, 'truncated', false),
  };
}

function parseGraphSourcePage(raw: unknown): GraphSourcePage {
  const obj = expectRecord(raw, 'GraphSourcePage');
  return {
    documentId: reqString(obj, 'document_id'),
    title: reqString(obj, 'title'),
    pageStart: optNumber(obj, 'page_start', 0),
  };
}

export function parseGraphNodeDetail(raw: unknown): GraphNodeDetail {
  const obj = expectRecord(raw, 'GraphNodeDetail');
  return {
    id: reqString(obj, 'id'),
    name: reqString(obj, 'name'),
    type: reqString(obj, 'type'),
    documentId: optNullableString(obj, 'document_id'),
    sources: reqArray(obj, 'sources').map(parseGraphSourcePage),
  };
}

function parseGraphEvidence(raw: unknown): GraphEvidence {
  const obj = expectRecord(raw, 'GraphEvidence');
  return {
    documentId: reqString(obj, 'document_id'),
    chunkId: reqString(obj, 'chunk_id'),
    pageStart: optNumber(obj, 'page_start', 0),
    sentence: reqString(obj, 'sentence'),
  };
}

export function parseGraphEdgeDetail(raw: unknown): GraphEdgeDetail {
  const obj = expectRecord(raw, 'GraphEdgeDetail');
  return {
    id: reqString(obj, 'id'),
    type: reqString(obj, 'type'),
    source: reqString(obj, 'source'),
    target: reqString(obj, 'target'),
    evidence: reqArray(obj, 'evidence').map(parseGraphEvidence),
  };
}

function parseGraphStage(raw: unknown): GraphStage {
  const obj = expectRecord(raw, 'GraphStage');
  return {
    name: reqString(obj, 'name'),
    status: reqString(obj, 'status'),
  };
}

export function parseGraphExtraction(raw: unknown): GraphExtraction {
  const obj = expectRecord(raw, 'GraphExtraction');
  return {
    graphStatus: parseGraphStatus(obj.graph_status),
    failureReason: optNullableString(obj, 'failure_reason'),
    manualRetryCount: optNumber(obj, 'manual_retry_count', 0),
    stages: reqArray(obj, 'stages').map(parseGraphStage),
  };
}

export function parseUnresolvedReferences(raw: unknown): UnresolvedReferences {
  if (!Array.isArray(raw)) {
    throw new Error('未匹配引用格式无效');
  }
  return raw.map((item) => {
    if (typeof item !== 'string') {
      throw new Error('未匹配引用格式无效');
    }
    return item;
  });
}

export function parseCitation(raw: unknown): Citation {
  const obj = expectRecord(raw, 'Citation');
  return {
    chunkId: optNullableString(obj, 'chunk_id'),
    documentId: optNullableString(obj, 'document_id'),
    label: optString(obj, 'label', ''),
    origin: optString(obj, 'origin', 'kb'),
    graphSupplement: optBoolean(obj, 'graph_supplement', false),
  };
}

function parseQATurn(raw: unknown): QATurn {
  const obj = expectRecord(raw, 'QATurn');
  const rawCites = Array.isArray(obj.citations) ? obj.citations : [];
  return {
    id: reqString(obj, 'id'),
    question: optString(obj, 'question', ''),
    answer: optString(obj, 'answer', ''),
    answerKind: parseAnswerKind(obj.answer_kind),
    rewritten: optBoolean(obj, 'rewritten', false),
    reranked: optBoolean(obj, 'reranked', false),
    createdAt: optString(obj, 'created_at', ''),
    citations: rawCites.map(parseCitation),
  };
}

export function parseQASession(raw: unknown): QASession {
  const obj = expectRecord(raw, 'QASession');
  const rawTurns = Array.isArray(obj.turns) ? obj.turns : [];
  return {
    id: reqString(obj, 'id'),
    title: optString(obj, 'title', '新会话'),
    createdAt: optString(obj, 'created_at', ''),
    updatedAt: optString(obj, 'updated_at', ''),
    turns: rawTurns.map(parseQATurn),
  };
}

export function parseQASessionPage(raw: unknown): PageResult<QASession> {
  const obj = expectRecord(raw, 'QASessionPage');
  const items = Array.isArray(obj.items) ? obj.items.map(parseQASession) : [];
  return {
    items,
    page: optNumber(obj, 'page', 1),
    pageSize: optNumber(obj, 'page_size', 20),
    total: optNumber(obj, 'total', items.length),
  };
}

export function parseRetrievalItem(raw: unknown): RetrievalItem {
  const obj = expectRecord(raw, 'RetrievalItem');
  return {
    alias: optString(obj, 'alias', ''),
    chunkId: reqString(obj, 'chunk_id'),
    documentId: optString(obj, 'document_id', ''),
    title: optString(obj, 'title', ''),
    page: optNumber(obj, 'page', 1),
    label: optString(obj, 'label', ''),
    graphSupplement: optBoolean(obj, 'graph_supplement', false),
  };
}

export function parseModelConfig(raw: unknown): ModelConfig {
  const obj = expectRecord(raw, 'ModelConfig');
  return {
    kind: parseModelKind(obj.kind),
    baseUrl: optString(obj, 'base_url', ''),
    modelName: optString(obj, 'model_name', ''),
    apiKey: optString(obj, 'api_key', ''),
    dimension: optNullableNumber(obj, 'dimension'),
  };
}

export function parseModelConfigList(raw: unknown): ModelConfig[] {
  const obj = expectRecord(raw, 'ModelConfigList');
  const items = Array.isArray(obj.items) ? obj.items : [];
  return items.map(parseModelConfig);
}

export function parseProbeResult(raw: unknown): ProbeResult {
  const obj = expectRecord(raw, 'ProbeResult');
  return {
    result: parseProbeStatus(obj.result),
    message: optString(obj, 'message', ''),
    elapsedMs: optNumber(obj, 'elapsed_ms', 0),
  };
}

export function serializeScope(scope: QAScope): UnknownRecord {
  switch (scope.type) {
    case 'all':
      return { type: 'all' };
    case 'kb':
      return { type: 'kb', kb_id: scope.kbId };
    case 'tags':
      return { type: 'tags', tag_ids: scope.tagIds };
    case 'documents':
      return { type: 'documents', document_ids: scope.documentIds };
  }
}
