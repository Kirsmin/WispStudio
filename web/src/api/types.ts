// 后端协议类型：字段名与 internal/api 的 JSON tag 保持一致（PascalCase）。

export interface EventDTO {
  ID: string
  Seq: number
  Type: string
  SessionID: string
  TurnID: string
  Actor: string
  ParentID: string
  CauseID: string
  SubjectID: string
  Blob: string
  Attrs: Record<string, unknown>
  TS: number
  BlobText?: string
}

export interface SessionDTO {
  ID: string
  SessionID: string
  Title: string
  Model?: string
  CreatedAt: number
  UpdatedAt: number
  MessageCount: number
  TurnCount: number
  LastTurnID?: string
  Running: boolean
}

export interface ModelDTO {
  ProviderID: string
  ModelID: string
  FullID: string
  Alias?: string
  Discovered: boolean
}

export interface ProviderDTO {
  ProviderID: string
  ProviderName?: string
  Models: ModelDTO[]
}

export interface ModelsResponse {
  DefaultModel?: string
  Providers: ProviderDTO[]
}

export interface ConfigDTO {
  Path: string
  Text: string
}

export interface StopDTO {
  SessionID: string
  TurnID?: string
  Stopped: boolean
}

export interface TurnStartedDTO {
  SessionID: string
  ContextID: string
  TurnID: string
  Model: string
}
