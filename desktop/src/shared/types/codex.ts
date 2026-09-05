export type CodexPolicyCeiling = 'read-only' | 'workspace-write'

export type CodexWorkerState =
    | 'disabled'
    | 'setup_required'
    | 'starting'
    | 'ready'
    | 'busy'
    | 'unauthorized'
    | 'incompatible'
    | 'failed'
    | 'stopped'

export type CodexRegistrationState =
    | 'unregistered'
    | 'registered'
    | 'waiting_for_main'
    | 'connected'
    | 'failed'

export interface CodexConfig {
    schemaVersion: 1
    enabled: boolean
    workspaceRoot: string
    stateRoot: string
    codexExecutable: string
    policyCeiling: CodexPolicyCeiling
    account: string
    installationId: string
    workerInstanceId: string
    policyRevision: number
    registrationName: string
}

export interface CodexWorkerStateSnapshot {
    state: CodexWorkerState
    enabled: boolean
    policyCeiling: CodexPolicyCeiling
    endpoint: string | null
    workerInstanceId: string | null
    bootEpoch: number | null
    policyRevision: number
    error?: string
}

export interface CodexRegistrationSnapshot {
    state: CodexRegistrationState
    path: string
    command: string | null
    args: string[]
    fingerprint: string | null
    error?: string
}

export interface CodexDesktopState {
    worker: CodexWorkerStateSnapshot
    registration: CodexRegistrationSnapshot
}

export interface CodexWorkerConfigInput {
    workspaceRoot: string
    codexExecutable?: string
    policyCeiling?: CodexPolicyCeiling
    account?: string
}

export interface CodexMcpRegistration {
    command: string
    args: string[]
}

export interface CodexTaskMetadata {
    taskId: string
    requestId: string
    attemptId: string
    leaseEpoch: number
    workerId: string
    state: string
    lastError?: string
    createdAt: string
    updatedAt: string
}

export interface CodexTaskReference {
    taskId: string
    attemptId: string
    leaseEpoch: number
}
